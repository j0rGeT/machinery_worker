package amqp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/streadway/amqp"
	"machinery_worker/v1/brokers/errs"
	"machinery_worker/v1/brokers/iface"
	"machinery_worker/v1/common"
	"machinery_worker/v1/config"
	"machinery_worker/v1/log"
	"machinery_worker/v1/tasks"

	redisbackend "machinery_worker/v1/backends/redis"
)

type AMQPConnection struct {
	queueName    string
	connection   *amqp.Connection
	channel      *amqp.Channel
	queue        amqp.Queue
	confirmation <-chan amqp.Confirmation
	errorchan    <-chan *amqp.Error
	cleanup      chan struct{}
}

// Broker represents an AMQP broker
type Broker struct {
	common.Broker
	common.AMQPConnector
	processingWG sync.WaitGroup // use wait group to make sure task processing completes on interrupt signal
	callbackProcessingWG sync.WaitGroup
	connections      map[string]*AMQPConnection
	connectionsMutex sync.RWMutex
}

// New creates new Broker instance
func New(cnf *config.Config) iface.Broker {
	return &Broker{Broker: common.NewBroker(cnf), AMQPConnector: common.AMQPConnector{}, connections: make(map[string]*AMQPConnection)}
}

// StartConsuming enters a loop and waits for incoming messages
func (b *Broker) StartConsuming(consumerTag string, concurrency int, taskProcessor iface.TaskProcessor) (bool, error) {
	b.Broker.StartConsuming(consumerTag, concurrency, taskProcessor)

	queueName := taskProcessor.CustomQueue()
	if queueName == "" {
		queueName = b.GetConfig().DefaultQueue
	}

	conn, channel, queue, _, amqpCloseChan, err := b.Connect(
		b.GetConfig().Broker,
		b.GetConfig().MultipleBrokerSeparator,
		b.GetConfig().TLSConfig,
		b.GetConfig().AMQP.Exchange,     // exchange name
		b.GetConfig().AMQP.ExchangeType, // exchange type
		queueName,                       // queue name
		true,                            // queue durable
		false,                           // queue delete when unused
		b.GetConfig().AMQP.BindingKey,   // queue binding key
		nil,                             // exchange declare args
		amqp.Table(b.GetConfig().AMQP.QueueDeclareArgs), // queue declare args
		amqp.Table(b.GetConfig().AMQP.QueueBindingArgs), // queue binding args
	)
	if err != nil {
		b.GetRetryFunc()(b.GetRetryStopChan())
		return b.GetRetry(), err
	}
	defer b.Close(channel, conn)

	if err = channel.Qos(
		b.GetConfig().AMQP.PrefetchCount,
		0,     // prefetch size
		false, // global
	); err != nil {
		return b.GetRetry(), fmt.Errorf("Channel qos error: %s", err)
	}

	deliveries, err := channel.Consume(
		queue.Name,  // queue
		consumerTag, // consumer tag
		false,       // auto-ack
		false,       // exclusive
		false,       // no-local
		false,       // no-wait
		nil,         // arguments
	)
	if err != nil {
		return b.GetRetry(), fmt.Errorf("Queue consume error: %s", err)
	}

	log.INFO.Print("[*] Waiting for messages. To exit press CTRL+C")

	//轮询回调函数
	//go func() {
	//	err := b.callbackConsume(deliveries2, concurrency, taskProcessor, amqpCloseChan)
	//	if err != nil {
	//		fmt.Println(err)
	//	}
	//}()

	if err := b.consume(deliveries, concurrency, taskProcessor, amqpCloseChan); err != nil {
		return b.GetRetry(), err
	}

	// Waiting for any tasks being processed to finish
	b.processingWG.Wait()

	return b.GetRetry(), nil
}

// StopConsuming quits the loop
func (b *Broker) StopConsuming() {
	b.Broker.StopConsuming()

	// Waiting for any tasks being processed to finish
	b.processingWG.Wait()
}

// GetOrOpenConnection will return a connection on a particular queue name. Open connections
// are saved to avoid having to reopen connection for multiple queues
func (b *Broker) GetOrOpenConnection(queueName string, queueBindingKey string, exchangeDeclareArgs, queueDeclareArgs, queueBindingArgs amqp.Table) (*AMQPConnection, error) {
	var err error

	b.connectionsMutex.Lock()
	defer b.connectionsMutex.Unlock()

	conn, ok := b.connections[queueName]
	if !ok {
		conn = &AMQPConnection{
			queueName: queueName,
			cleanup:   make(chan struct{}),
		}
		conn.connection, conn.channel, conn.queue, conn.confirmation, conn.errorchan, err = b.Connect(
			b.GetConfig().Broker,
			b.GetConfig().MultipleBrokerSeparator,
			b.GetConfig().TLSConfig,
			b.GetConfig().AMQP.Exchange,     // exchange name
			b.GetConfig().AMQP.ExchangeType, // exchange type
			queueName,                       // queue name
			true,                            // queue durable
			false,                           // queue delete when unused
			queueBindingKey,                 // queue binding key
			exchangeDeclareArgs,             // exchange declare args
			queueDeclareArgs,                // queue declare args
			queueBindingArgs,                // queue binding args
		)
		if err != nil {
			return nil, errors.Wrapf(err, "Failed to connect to queue %s", queueName)
		}

		// Reconnect to the channel if it disconnects/errors out
		go func() {
			select {
			case err = <-conn.errorchan:
				log.INFO.Printf("Error occurred on queue: %s. Reconnecting", queueName)
				b.connectionsMutex.Lock()
				delete(b.connections, queueName)
				b.connectionsMutex.Unlock()
				_, err := b.GetOrOpenConnection(queueName, queueBindingKey, exchangeDeclareArgs, queueDeclareArgs, queueBindingArgs)
				if err != nil {
					log.ERROR.Printf("Failed to reopen queue: %s.", queueName)
				}
			case <-conn.cleanup:
				return
			}
			return
		}()
		b.connections[queueName] = conn
	}
	return conn, nil
}

func (b *Broker) CloseConnections() error {
	b.connectionsMutex.Lock()
	defer b.connectionsMutex.Unlock()

	for key, conn := range b.connections {
		if err := b.Close(conn.channel, conn.connection); err != nil {
			log.ERROR.Print("Failed to close channel")
			return nil
		}
		close(conn.cleanup)
		delete(b.connections, key)
	}
	return nil
}

// Publish places a new message on the default queue
func (b *Broker) Publish(ctx context.Context, signature *tasks.Signature) error {
	// Adjust routing key (this decides which queue the message will be published to)
	b.AdjustRoutingKey(signature)

	msg, err := json.Marshal(signature)
	if err != nil {
		return fmt.Errorf("JSON marshal error: %s", err)
	}

	// Check the ETA signature field, if it is set and it is in the future,
	// delay the task
	if signature.ETA != nil {
		now := time.Now().UTC()

		if signature.ETA.After(now) {
			delayMs := int64(signature.ETA.Sub(now) / time.Millisecond)

			return b.delay(signature, delayMs)
		}
	}

	queue := b.GetConfig().DefaultQueue
	bindingKey := b.GetConfig().AMQP.BindingKey // queue binding key
	if b.isDirectExchange() {
		queue = signature.RoutingKey
		bindingKey = signature.RoutingKey
	}

	connection, err := b.GetOrOpenConnection(
		queue,
		bindingKey, // queue binding key
		nil,        // exchange declare args
		amqp.Table(b.GetConfig().AMQP.QueueDeclareArgs), // queue declare args
		amqp.Table(b.GetConfig().AMQP.QueueBindingArgs), // queue binding args
	)
	if err != nil {
		return errors.Wrapf(err, "Failed to get a connection for queue %s", queue)
	}

	channel := connection.channel
	confirmsChan := connection.confirmation

	if err := channel.Publish(
		b.GetConfig().AMQP.Exchange, // exchange name
		signature.RoutingKey,        // routing key
		false,                       // mandatory
		false,                       // immediate
		amqp.Publishing{
			Headers:      amqp.Table(signature.Headers),
			ContentType:  "application/json",
			Body:         msg,
			Priority:     signature.Priority,
			DeliveryMode: amqp.Persistent,
		},
	); err != nil {
		return errors.Wrap(err, "Failed to publish task")
	}

	confirmed := <-confirmsChan

	if confirmed.Ack {
		return nil
	}

	return fmt.Errorf("Failed delivery of delivery tag: %v", confirmed.DeliveryTag)
}

// ParseRedisURL ...
func ParseRedisURL(url string) (host, password string, db int, err error) {
	// redis://pwd@host/db

	var u *neturl.URL
	u, err = neturl.Parse(url)
	if err != nil {
		return
	}
	if u.Scheme != "redis" {
		err = errors.New("No redis scheme found")
		return
	}

	if u.User != nil {
		var exists bool
		password, exists = u.User.Password()
		if !exists {
			password = u.User.Username()
		}
	}

	host = u.Host

	parts := strings.Split(u.Path, "/")
	if len(parts) == 1 {
		db = 0 //default redis db
	} else {
		db, err = strconv.Atoi(parts[1])
		if err != nil {
			db, err = 0, nil //ignore err here
		}
	}

	return
}


func (b *Broker) callbackConsume(deliveries <-chan amqp.Delivery, concurrency int, taskProcessor iface.TaskProcessor, amqpCloseChan <-chan *amqp.Error) error {
	// make channel with a capacity makes it become a buffered channel so that a worker which wants to
	// push an error to `errorsChan` doesn't need to be blocked while the for-loop is blocked waiting
	// a worker, that is, it avoids a possible deadlock
	errorsChan := make(chan error, 1)

	for {
		select {
		case amqpErr := <-amqpCloseChan:
			return amqpErr
		case err := <-errorsChan:
			return err
		case d := <-deliveries:
			b.callbackProcessingWG.Add(1)

			// Consume the task inside a gotourine so multiple tasks
			// can be processed concurrently
			go func() {
				if err := b.callbackConsumeOne(d, taskProcessor, true); err != nil {
					errorsChan <- err
				}

				b.callbackProcessingWG.Done()

			}()
		case <-b.GetStopChan():
			return nil
		}
	}
}

// consume takes delivered messages from the channel and manages a worker pool
// to process tasks concurrently
func (b *Broker) consume(deliveries <-chan amqp.Delivery, concurrency int, taskProcessor iface.TaskProcessor, amqpCloseChan <-chan *amqp.Error) error {
	pool := make(chan struct{}, concurrency)
	current_avail_task_count := 0

	// initialize worker pool with maxWorkers workers
	go func() {
		for i := 0; i < concurrency; i++ {
			pool <- struct{}{}
			current_avail_task_count += 1
		}
	}()

	// make channel with a capacity makes it become a buffered channel so that a worker which wants to
	// push an error to `errorsChan` doesn't need to be blocked while the for-loop is blocked waiting
	// a worker, that is, it avoids a possible deadlock
	errorsChan := make(chan error, 1)

	for {
		select {
		case amqpErr := <-amqpCloseChan:
			return amqpErr
		case err := <-errorsChan:
			return err
		case d := <-deliveries:
			//轮训检查pending任务是否完成。
			b.processingWG.Add(1)
			count := 0
			for {
				message := new(*tasks.Signature)

				log.DEBUG.Printf("Received new message: %s", d.Body)
				err := json.Unmarshal(d.Body, message)
				redisHost, redisPassword, redisDB, err := ParseRedisURL(b.GetConfig().ResultBackend)
				if err != nil {
					fmt.Println(err)
				}

				redisbackendObj := redisbackend.New(b.GetConfig(), redisHost, redisPassword, "", redisDB)
				executingTaskCount := redisbackendObj.GetExecutingTasksLen()
				if concurrency > 0 && executingTaskCount < concurrency && (*message).Name == "requestCreateVM"{
					break
				}
				if (*message).Name == "CreateVMCallback" {
					break
				}

				//获取当前id状态，如果已经成功，过滤，对这种情况进行处理
				taskState, err := redisbackendObj.GetExecutingTasksStatus((*message).UUID)
				if taskState == tasks.StateSuccess {
					break
				}

				time.Sleep(time.Duration(time.Second*10))
				// 检查轮询测试
				count += 1
				if count > 20 {
					fmt.Println("超时返回")
					break
				}
			}

			if concurrency > 0{
				<-pool
				current_avail_task_count -= 1
			}

			// Consume the task inside a gotourine so multiple tasks
			// can be processed concurrently
			go func() {
				if err := b.consumeOne(d, taskProcessor, true); err != nil {
					errorsChan <- err
				}

				b.processingWG.Done()


				if concurrency > 0 {
					// give worker back to pool
					pool <- struct{}{}
					current_avail_task_count += 1
				}

			}()
		case <-b.GetStopChan():
			return nil
		}
	}
}

// consumeOne processes a single message using TaskProcessor
func (b *Broker) consumeOne(delivery amqp.Delivery, taskProcessor iface.TaskProcessor, ack bool) error {
	if len(delivery.Body) == 0 {
		delivery.Nack(true, false)                     // multiple, requeue
		return errors.New("Received an empty message") // RabbitMQ down?
	}

	var multiple, requeue = false, false

	// Unmarshal message body into signature struct
	signature := new(tasks.Signature)
	decoder := json.NewDecoder(bytes.NewReader(delivery.Body))
	decoder.UseNumber()
	if err := decoder.Decode(signature); err != nil {
		delivery.Nack(multiple, requeue)
		return errs.NewErrCouldNotUnmarshalTaskSignature(delivery.Body, err)
	}

	// If the task is not registered, we nack it and requeue,
	// there might be different workers for processing specific tasks
	if !b.IsTaskRegistered(signature.Name) {
		requeue = true
		log.INFO.Printf("Task not registered with this worker. Requeing message: %s", delivery.Body)

    if !signature.IgnoreWhenTaskNotRegistered {
			delivery.Nack(multiple, requeue)
		}
    
		return nil
	}

	message := new(*tasks.Signature)

	log.DEBUG.Printf("Received new message: %s", delivery.Body)
	err1 := json.Unmarshal(delivery.Body, message)
	if err1 != nil {
		log.DEBUG.Printf("catch task type is ", (*message).Name)
	}

	err := taskProcessor.Process(signature)
	if ack {
		delivery.Ack(multiple)
	}
	return err

}

// consumeOne processes a single message using TaskProcessor
func (b *Broker) callbackConsumeOne(delivery amqp.Delivery, taskProcessor iface.TaskProcessor, ack bool) error {
	if len(delivery.Body) == 0 {
		delivery.Nack(true, false)                     // multiple, requeue
		return errors.New("Received an empty message") // RabbitMQ down?
	}

	var multiple, requeue = false, false

	// Unmarshal message body into signature struct
	signature := new(tasks.Signature)
	decoder := json.NewDecoder(bytes.NewReader(delivery.Body))
	decoder.UseNumber()
	if err := decoder.Decode(signature); err != nil {
		delivery.Nack(multiple, requeue)
		return errs.NewErrCouldNotUnmarshalTaskSignature(delivery.Body, err)
	}

	// If the task is not registered, we nack it and requeue,
	// there might be different workers for processing specific tasks
	if !b.IsTaskRegistered(signature.Name) {
		requeue = true
		log.INFO.Printf("Task not registered with this worker. Requeing message: %s", delivery.Body)

		if !signature.IgnoreWhenTaskNotRegistered {
			delivery.Nack(multiple, requeue)
		}

		return nil
	}

	message := new(*tasks.Signature)

	//log.DEBUG.Printf("Received new message: %s", delivery.Body)
	err1 := json.Unmarshal(delivery.Body, message)
	if err1 != nil {
		log.DEBUG.Printf("catch task type is ", (*message).Name)
	}

	if (*message).Name != "CreateVMCallback" {
		log.DEBUG.Printf("Received new message: %s", (*message).Name)
		delivery.Ack(false)
		return nil
	} else{
		err := taskProcessor.Process(signature)
		if ack {
			delivery.Ack(multiple)
		}
		return err
	}
}



// delay a task by delayDuration miliseconds, the way it works is a new queue
// is created without any consumers, the message is then published to this queue
// with appropriate ttl expiration headers, after the expiration, it is sent to
// the proper queue with consumers
func (b *Broker) delay(signature *tasks.Signature, delayMs int64) error {
	if delayMs <= 0 {
		return errors.New("Cannot delay task by 0ms")
	}

	message, err := json.Marshal(signature)
	if err != nil {
		return fmt.Errorf("JSON marshal error: %s", err)
	}

	// It's necessary to redeclare the queue each time (to zero its TTL timer).
	queueName := fmt.Sprintf(
		"delay.%d.%s.%s",
		delayMs, // delay duration in mileseconds
		b.GetConfig().AMQP.Exchange,
		signature.RoutingKey, // routing key
	)
	declareQueueArgs := amqp.Table{
		// Exchange where to send messages after TTL expiration.
		"x-dead-letter-exchange": b.GetConfig().AMQP.Exchange,
		// Routing key which use when resending expired messages.
		"x-dead-letter-routing-key": signature.RoutingKey,
		// Time in milliseconds
		// after that message will expire and be sent to destination.
		"x-message-ttl": delayMs,
		// Time after that the queue will be deleted.
		"x-expires": delayMs * 2,
	}
	conn, channel, _, _, _, err := b.Connect(
		b.GetConfig().Broker,
		b.GetConfig().MultipleBrokerSeparator,
		b.GetConfig().TLSConfig,
		b.GetConfig().AMQP.Exchange,     // exchange name
		b.GetConfig().AMQP.ExchangeType, // exchange type
		queueName,                       // queue name
		true,                            // queue durable
		b.GetConfig().AMQP.AutoDelete,   // queue delete when unused
		queueName,                       // queue binding key
		nil,                             // exchange declare args
		declareQueueArgs,                // queue declare args
		amqp.Table(b.GetConfig().AMQP.QueueBindingArgs), // queue binding args
	)
	if err != nil {
		return err
	}

	defer b.Close(channel, conn)

	if err := channel.Publish(
		b.GetConfig().AMQP.Exchange, // exchange
		queueName,                   // routing key
		false,                       // mandatory
		false,                       // immediate
		amqp.Publishing{
			Headers:      amqp.Table(signature.Headers),
			ContentType:  "application/json",
			Body:         message,
			DeliveryMode: amqp.Persistent,
		},
	); err != nil {
		return err
	}

	return nil
}

func (b *Broker) isDirectExchange() bool {
	return b.GetConfig().AMQP != nil && b.GetConfig().AMQP.ExchangeType == "direct"
}

// AdjustRoutingKey makes sure the routing key is correct.
// If the routing key is an empty string:
// a) set it to binding key for direct exchange type
// b) set it to default queue name
func (b *Broker) AdjustRoutingKey(s *tasks.Signature) {
	if s.RoutingKey != "" {
		return
	}

	if b.isDirectExchange() {
		// The routing algorithm behind a direct exchange is simple - a message goes
		// to the queues whose binding key exactly matches the routing key of the message.
		s.RoutingKey = b.GetConfig().AMQP.BindingKey
		return
	}

	s.RoutingKey = b.GetConfig().DefaultQueue
}

// Helper type for GetPendingTasks to accumulate signatures
type sigDumper struct {
	customQueue string
	Signatures  []*tasks.Signature
}

func (s *sigDumper) Process(sig *tasks.Signature) error {
	s.Signatures = append(s.Signatures, sig)
	return nil
}

func (s *sigDumper) CustomQueue() string {
	return s.customQueue
}

func (_ *sigDumper) PreConsumeHandler() bool {
	return true
}

func (b *Broker) GetPendingTasks(queue string) ([]*tasks.Signature, error) {
	if queue == "" {
		queue = b.GetConfig().DefaultQueue
	}

	bindingKey := b.GetConfig().AMQP.BindingKey // queue binding key
	conn, err := b.GetOrOpenConnection(
		queue,
		bindingKey, // queue binding key
		nil,        // exchange declare args
		amqp.Table(b.GetConfig().AMQP.QueueDeclareArgs), // queue declare args
		amqp.Table(b.GetConfig().AMQP.QueueBindingArgs), // queue binding args
	)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to get a connection for queue %s", queue)
	}

	channel := conn.channel
	queueInfo, err := channel.QueueInspect(queue)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to get info for queue %s", queue)
	}

	var tag uint64
	defer channel.Nack(tag, true, true) // multiple, requeue

	dumper := &sigDumper{customQueue: queue}
	for i := 0; i < queueInfo.Messages; i++ {
		d, _, err := channel.Get(queue, false)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to get from queue")
		}
		tag = d.DeliveryTag
		b.consumeOne(d, dumper, false)
	}

	return dumper.Signatures, nil
}
