package webdav

import (
	"github.com/studio-b12/gowebdav"
	"sync"
)

// Config 结构体用于存储 WebDAV 连接信息。
type Config struct {
	Endpoint   string `yaml:"endpoint"`
	User       string `yaml:"user"`
	Password   string `yaml:"password"`
	CustomPath string `yaml:"custom-path"`
}

// WebDAV 结构体表示 WebDAV 客户端。
type WebDAV struct {
	Client *gowebdav.Client
	Config *Config
}

var clients = make(map[Config]*WebDAV)
var clientsMu sync.Mutex

// NewClient 创建一个新的 WebDAV 客户端实例。
func NewClient(conf *Config) (*WebDAV, error) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	snapshot := *conf
	conf = &snapshot
	var endpoint = conf.Endpoint
	var user = conf.User
	key := *conf

	if clients[key] != nil {
		return clients[key], nil
	}

	c := gowebdav.NewClient(endpoint, user, conf.Password)
	c.Connect()

	clients[key] = &WebDAV{
		Client: c,
		Config: conf,
	}
	return clients[key], nil
}
