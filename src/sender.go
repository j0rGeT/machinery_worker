package main

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"log"
	//"github.com/gin-gonic/gin"
	"machinery_worker/v1"
	"machinery_worker/v1/config"
	"machinery_worker/v1/tasks"
	"time"
)

// mock create virtualmachine
func mockCreateVM(isNeedCreateVM bool, server *machinery.Server, signature *tasks.Signature) {
	if isNeedCreateVM == true {
		//create virtualMachine
		time.Sleep(time.Duration(time.Second * 5))
	} else {
		time.Sleep(time.Duration(time.Second * 5))

	}
	AsyncResult2, err := server.SendTaskWithContext(context.Background(), signature)
	if err != nil {
		log.Fatal(err)
	}
	results2, err := AsyncResult2.Get(time.Duration(time.Millisecond * 5))
	fmt.Println("results %s", results2)
}

func callbackSender(key string) string {
	var callback_cnf = &config.Config{
		Broker:        "amqp://guest:guest@localhost:5672/",
		DefaultQueue:  "machinery_tasks_callback",
		ResultBackend: "redis://127.0.0.1:6379",
		AMQP: &config.AMQPConfig{
			Exchange:     "machinery_exchange_callback",
			ExchangeType: "direct",
			BindingKey:   "machinery_task_callback",
		},
	}

	//init server
	server, err := machinery.NewServer(callback_cnf)
	if err != nil {
		log.Fatal(err)
	}
	var (
		CreateVMCallback tasks.Signature
	)

	batchID := uuid.New().String()

	var initTasks = func() {
		//task signature
		CreateVMCallback = tasks.Signature{
			Name: "CreateVMCallback",
			Args: []tasks.Arg{
				{
					Name:  "uuid",
					Type:  "string",
					Value: batchID,
				},
			},
			UUID: key,
		}
	}

	initTasks()

	AsyncResult, err := server.SendTask(&CreateVMCallback)

	results, err := AsyncResult.Get(time.Duration(time.Second * 20))
	fmt.Println("results %s", results)

	return tasks.HumanReadableResults(results)

}



func startSender(key string) string {
	var cnf = &config.Config{
		Broker:        "amqp://guest:guest@localhost:5672/",
		DefaultQueue:  "machinery_tasks",
		ResultBackend: "redis://127.0.0.1:6379",
		AMQP: &config.AMQPConfig{
			Exchange:     "machinery_exchange",
			ExchangeType: "direct",
			BindingKey:   "machinery_task",
		},
	}

	//init server
	server, err := machinery.NewServer(cnf)
	if err != nil {
		log.Fatal(err)
	}

	var (
		requestCreateVM tasks.Signature
	)

	batchID := uuid.New().String()

	var initTasks = func() {
		//task signature

		requestCreateVM = tasks.Signature{
			Name: "requestCreateVM",
			Args: []tasks.Arg{
				{
					Name:  "uuid",
					Type:  "string",
					Value: batchID,
				},
			},
			UUID: key,
		}
	}

	initTasks()

	fmt.Println(requestCreateVM)

	AsyncResult, err := server.SendTask(&requestCreateVM)

	results, err := AsyncResult.Get(time.Duration(time.Second * 20))
	fmt.Println("results %s", results)

	return tasks.HumanReadableResults(results)

}

func main() {
	//r := gin.Default()
	//r.GET("/createVirtualMachine", startSender)
	//r.Run()
	taskMap := []string{
		"task1",
		"task2",
		"task3",
		"task4",
		"task5",
		"task6",
		"task7",
		"task8",
		"task9",
		"task10",
	}
	for _, taskName := range taskMap {
		fmt.Println(taskName)
		go func(){
			key := uuid.New().String()
			chan_channel := make(chan string)
			chan_channel <- startSender(key)
			time.Sleep(time.Duration(time.Second*60))
			info := <- chan_channel
			if info != "" {
				callbackSender(key)
			}
		}()
	}
	time.Sleep(time.Duration(time.Second*100))
}
