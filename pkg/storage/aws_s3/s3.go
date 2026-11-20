package aws_s3

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/pkg/errors"
)

func cacheKey(conf *Config) Config {
	return *conf
}

type Config struct {
	Region          string `yaml:"region"`
	BucketName      string `yaml:"bucket-name"`
	AccessKeyID     string `yaml:"access-key-id"`
	AccessKeySecret string `yaml:"access-key-secret"`
	CustomPath      string `yaml:"custom-path"`
}

type S3 struct {
	S3Client        *s3.Client
	TransferManager *transfermanager.Client
	Config          *Config
}

var clients = make(map[Config]*S3)
var clientsMu sync.Mutex

func NewClient(conf *Config) (*S3, error) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	snapshot := *conf
	conf = &snapshot
	var region = conf.Region
	var accessKeyId = conf.AccessKeyID
	var accessKeySecret = conf.AccessKeySecret

	key := cacheKey(conf)
	if clients[key] != nil {
		return clients[key], nil
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyId, accessKeySecret, "")),
		config.WithRegion(region),
	)
	if err != nil {
		return nil, errors.Wrap(err, "aws_s3")
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {})

	clients[key] = &S3{
		S3Client:        client,
		TransferManager: transfermanager.New(client),
		Config:          conf,
	}
	return clients[key], nil
}
