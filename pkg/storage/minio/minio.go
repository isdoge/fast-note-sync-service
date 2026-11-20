package minio

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
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
	BucketName      string `yaml:"bucket-name"`
	Endpoint        string `yaml:"endpoint"`
	Region          string `yaml:"region"`
	AccessKeyID     string `yaml:"access-key-id"`
	AccessKeySecret string `yaml:"access-key-secret"`
	CustomPath      string `yaml:"custom-path"`
}

type MinIO struct {
	S3Client        *s3.Client
	TransferManager *transfermanager.Client
	Config          *Config
}

var clients = make(map[Config]*MinIO)
var clientsMu sync.Mutex

func NewClient(conf *Config) (*MinIO, error) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	snapshot := *conf
	conf = &snapshot
	var endpoint = conf.Endpoint
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
		return nil, errors.Wrap(err, "minio")
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})

	clients[key] = &MinIO{
		S3Client:        client,
		TransferManager: transfermanager.New(client),
		Config:          conf,
	}
	return clients[key], nil
}
