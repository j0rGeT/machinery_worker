package _func

import (
	"fmt"
	"machinery_worker/pkg/common"
	"time"
)

func RequestCreateVM(uuid string) (string, error) {
	fmt.Println("uuid is %s", uuid)
	fmt.Println("request create virtualmachine resource...")
	time.Sleep(time.Second * 3)
	fmt.Println("request create virtualmachine connect success")

	return "hi I will create virtual machine", nil
}

func CreateVMCallback(uuid string) (string, error) {
	fmt.Println("uuid is %s", uuid)
	fmt.Println("create virtualmachine resource...")
	time.Sleep(time.Second * 3)
	fmt.Println("create virtualmachine connect success")

	return "create virtual machine success", nil
}



func getTaskInfo(taskId string) bool {
	for _, currentTask := range common.CallbackQueue {
		if currentTask.TaskId == taskId {
			return true
		}
	}
	return false
}


func startCallbackWorker(taskId string) {
	//定时检查当前队列中是否已经完成
	go func() {
		for {
			if getTaskInfo(taskId) == true {
				break
			}
			time.Sleep(time.Duration(time.Second*10))
		}
	}()
}