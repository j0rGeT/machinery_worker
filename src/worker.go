package main

import (
	"fmt"
	"log"
	"machinery_worker/pkg/func"
	"machinery_worker/v1"
	"machinery_worker/v1/config"
	"machinery_worker/v1/tasks"
)



type resourceTask struct {
	id string
	name string
	taskStatus string
}

var globalTaskInfo []*resourceTask


func searchTaskInfo(uuid string) *resourceTask {
	for _, item := range globalTaskInfo {
		if item.id == uuid {
			return item
		}
	}
	return &resourceTask{}
}

func callbackStartWorker() {
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
	taskList := map[string]interface{}{
		"CreateVMCallback":				_func.CreateVMCallback,
	}


	callback_server, err := machinery.NewServer(callback_cnf)

	callback_server.RegisterTasks(taskList)

	callback_worker := callback_server.NewWorker("worker_callback@localhost", 2)

	err = callback_worker.Launch()
	if err != nil {
		log.Fatal(err)
	}
}


func startWorker() {
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

	taskList := map[string]interface{}{
		"requestCreateVM":              _func.RequestCreateVM,
	}

	//regist task
	server.RegisterTasks(taskList)


	//create worker
	worker := server.NewWorker("worker@localhost", 2)


	posttaskhandler := func(signature *tasks.Signature) {
		fmt.Println("post handler")
	}

	errorhandler := func(err error) {
		log.Println("I am an error handler:", err)
	}

	pretaskhandler := func(signature *tasks.Signature) {
		log.Println("I am a start of task handler for:", signature.Name)
	}

	worker.SetErrorHandler(errorhandler)
	worker.SetPostTaskHandler(posttaskhandler)
	worker.SetPreTaskHandler(pretaskhandler)

	err = worker.Launch()
	if err != nil {
		log.Fatal(err)
	}


}

func main() {
	go startWorker()
	callbackStartWorker()
}
