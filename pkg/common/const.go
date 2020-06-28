package common



type TaskInfo struct {
	TaskId 	string
	Status	string
	Name string
}

var CallbackQueue []*TaskInfo
