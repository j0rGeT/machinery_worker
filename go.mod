module machinery_worker

require (
	cloud.google.com/go/pubsub v1.2.0
	github.com/RichardKnop/logging v0.0.0-20190827224416-1a693bdd4fae
	github.com/RichardKnop/machinery v1.8.6
	github.com/RichardKnop/redsync v1.2.0
	github.com/aws/aws-sdk-go v1.29.15
	github.com/bradfitz/gomemcache v0.0.0-20190913173617-a41fca850d0b
	github.com/gin-gonic/gin v1.6.3
	github.com/go-redis/redis v6.15.7+incompatible
	github.com/gomodule/redigo v2.0.0+incompatible
	github.com/google/uuid v1.1.1
	github.com/kelseyhightower/envconfig v1.4.0
	github.com/opentracing/opentracing-go v1.1.0
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.4.2
	github.com/spf13/viper v1.7.0
	github.com/streadway/amqp v0.0.0-20200108173154-1c71cc93ed71
	github.com/stretchr/testify v1.4.0
	github.com/urfave/cli v1.22.2
	go.mongodb.org/mongo-driver v1.3.0
	gopkg.in/yaml.v2 v2.2.8
)

replace git.apache.org/thrift.git => github.com/apache/thrift v0.0.0-20180902110319-2566ecd5d999

go 1.13
