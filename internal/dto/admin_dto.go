package dto

import "time"

// AdminWebGUIConfig WebGUI configuration response structure (public interface)
// AdminWebGUIConfig WebGUI 配置响应结构（公开接口）
type AdminWebGUIConfig struct {
	FontSet          string `json:"fontSet"`          // Font set // 字体设置
	RegisterIsEnable bool   `json:"registerIsEnable"` // Registration enablement // 是否开启注册
	FtsBleveEnabled  bool   `json:"ftsBleveEnabled"`  // Whether Bleve FTS is enabled // 是否启用 Bleve 全文搜索
}

// AdminCheckResponse Admin check response structure
// AdminCheckResponse 管理员权限检查响应结构
type AdminCheckResponse struct {
	IsAdmin bool `json:"isAdmin"` // Whether have admin privileges // 是否具有管理员权限
}

// AdminConfig Admin configuration structure (admin interface)
// AdminConfig 管理员配置结构（管理员接口）
type AdminConfig struct {
	FontSet                 *string `json:"fontSet,omitempty" form:"fontSet"`                                 // Font set // 字体设置
	RegisterIsEnable        *bool   `json:"registerIsEnable,omitempty" form:"registerIsEnable"`               // Registration enablement // 是否开启注册
	FileChunkSize           *string `json:"fileChunkSize,omitempty" form:"fileChunkSize"`                     // File chunk size // 文件分块大小
	SoftDeleteRetentionTime *string `json:"softDeleteRetentionTime,omitempty" form:"softDeleteRetentionTime"` // Soft delete retention time // 软删除保留时间
	UploadSessionTimeout    *string `json:"uploadSessionTimeout,omitempty" form:"uploadSessionTimeout"`       // Upload session timeout // 上传会话超时时间
	HistoryKeepVersions     *int    `json:"historyKeepVersions,omitempty" form:"historyKeepVersions"`         // History versions to keep // 历史版本保留数
	HistorySaveDelay        *string `json:"historySaveDelay,omitempty" form:"historySaveDelay"`               // History save delay // 历史保存延迟
	DefaultAPIFolder        *string `json:"defaultApiFolder,omitempty" form:"defaultApiFolder"`               // Default API folder // 默认 API 目录
	AdminUID                *int    `json:"adminUid,omitempty" form:"adminUid"`                               // Admin UID // 管理员 UID
	AuthTokenKey            *string `json:"authTokenKey,omitempty" form:"authTokenKey"`                       // Auth token key // 认证 Token 密钥
	TokenExpiry             *string `json:"tokenExpiry,omitempty" form:"tokenExpiry"`                         // Token expiry // Token 有效期
	ShareTokenKey           *string `json:"shareTokenKey,omitempty" form:"shareTokenKey"`                     // Share token key // 分享 Token 密钥
	ShareTokenExpiry        *string `json:"shareTokenExpiry,omitempty" form:"shareTokenExpiry"`               // Share token expiry // 分享 Token 有效期
	PullSource              *string `json:"pullSource,omitempty" form:"pullSource"`                           // Data pull source: auto | github (legacy values use GitHub) // 数据拉取源：auto | github (legacy values use GitHub)
	PullReleaseChannel      *string `json:"pullReleaseChannel,omitempty" form:"pullReleaseChannel"`           // Update version channel: stable | beta // 更新版本通道：stable | beta
	WebGUILoginTokenExpiry  *string `json:"webguiLoginTokenExpiry,omitempty" form:"webguiLoginTokenExpiry"`   // WebGUI login token expiry // WebGUI 登录 Token 有效期
	WebGUILoginTokenBindIP  *bool   `json:"webguiLoginTokenBindIp,omitempty" form:"webguiLoginTokenBindIp"`   // WebGUI login token bind IP // WebGUI 登录 Token 是否绑定 IP
	WebGUILoginTokenBindUA  *bool   `json:"webguiLoginTokenBindUa,omitempty" form:"webguiLoginTokenBindUa"`   // WebGUI login token bind User-Agent // WebGUI 登录 Token 是否绑定 User-Agent
	CustomResponseHeaders   *map[string]string `json:"customResponseHeaders,omitempty" form:"customResponseHeaders"` // Custom HTTP response headers // 自定义 HTTP 响应头
	DefaultPageSize               *int               `json:"defaultPageSize,omitempty" form:"defaultPageSize"`                             // Default page size // 默认每页显示数
	MaxPageSize                   *int               `json:"maxPageSize,omitempty" form:"maxPageSize"`                                     // Max page size // 最大每页显示限制
	DefaultContextTimeout         *int               `json:"defaultContextTimeout,omitempty" form:"defaultContextTimeout"`                 // Default context timeout // 默认上下文超时
	TempPath                      *string            `json:"tempPath,omitempty" form:"tempPath"`                                           // Temporary file path // 临时文件路径
	IsReturnSussess               *bool              `json:"isReturnSussess,omitempty" form:"isReturnSussess"`                             // Whether to return success detail // 是否返回成功详情
	SyncLogRetentionTime          *string            `json:"syncLogRetentionTime,omitempty" form:"syncLogRetentionTime"`                   // Sync log retention time // 同步日志保留时长
	DownloadSessionTimeout        *string            `json:"downloadSessionTimeout,omitempty" form:"downloadSessionTimeout"`               // Download session timeout // 下载分片超时
	WorkerPoolMaxWorkers          *int               `json:"workerPoolMaxWorkers,omitempty" form:"workerPoolMaxWorkers"`                   // Worker pool max workers // 协程池最大协程数
	WorkerPoolQueueSize           *int               `json:"workerPoolQueueSize,omitempty" form:"workerPoolQueueSize"`                     // Worker pool queue size // 协程池队列大小
	WriteQueueCapacity            *int               `json:"writeQueueCapacity,omitempty" form:"writeQueueCapacity"`                       // Write queue capacity // 写入队列容量
	WriteQueueTimeout             *string            `json:"writeQueueTimeout,omitempty" form:"writeQueueTimeout"`                         // Write queue timeout // 写入队列超时
	WriteQueueIdleTime            *string            `json:"writeQueueIdleTime,omitempty" form:"writeQueueIdleTime"`                       // Write queue idle cleanup time // 写入队列空闲清理时长
	WebSocketReadMaxPayloadSize   *string            `json:"wsReadMaxPayloadSize,omitempty" form:"wsReadMaxPayloadSize"`                   // WebSocket max read payload // WebSocket 最大读取负载
	WebSocketWriteMaxPayloadSize  *string            `json:"wsWriteMaxPayloadSize,omitempty" form:"wsWriteMaxPayloadSize"`                 // WebSocket max write payload // WebSocket 最大写入负载
	WebSocketParallelEnabled      *bool              `json:"wsParallelEnabled,omitempty" form:"wsParallelEnabled"`                         // Whether ws parallel is enabled // WebSocket 并行处理是否开启
	WebSocketParallelGolimit      *int               `json:"wsParallelGolimit,omitempty" form:"wsParallelGolimit"`                         // Ws parallel goroutine limit // WebSocket 并行协程限制
	WebSocketCheckUtf8Enabled     *bool              `json:"wsCheckUtf8Enabled,omitempty" form:"wsCheckUtf8Enabled"`                       // Whether ws check UTF-8 is enabled // WebSocket 是否开启 UTF-8 校验
	WebSocketCompressionEnabled   *bool              `json:"wsCompressionEnabled,omitempty" form:"wsCompressionEnabled"`                   // Whether ws compression is enabled // WebSocket 是否开启压缩
	WebSocketCompressionLevel     *int               `json:"wsCompressionLevel,omitempty" form:"wsCompressionLevel"`                       // Ws compression level // WebSocket 压缩级别
	WebSocketCompressionThreshold *int               `json:"wsCompressionThreshold,omitempty" form:"wsCompressionThreshold"`               // Ws compression threshold // WebSocket 压缩阈值
	FtsBleveEnabled               *bool              `json:"ftsBleveEnabled,omitempty" form:"ftsBleveEnabled"`                             // Whether Bleve FTS is enabled // 是否启用 Bleve 全文搜索
	FtsBleveStoreRaw              *bool              `json:"ftsBleveStoreRaw,omitempty" form:"ftsBleveStoreRaw"`                           // Whether Bleve stores raw content // Bleve 全文搜索是否存储原始文本
	PipelineWindowUp              *int               `json:"pipelineWindowUp,omitempty" form:"pipelineWindowUp"`                           // Upload pipeline window size for pv>=2 connections; 0 = stop-and-wait // pv>=2 连接的上行流水线窗口大小；0 = stop-and-wait
	PipelineWindowDown            *int               `json:"pipelineWindowDown,omitempty" form:"pipelineWindowDown"`                       // Download pipeline window size for pv>=2 connections; 0 = stop-and-wait // pv>=2 连接的下行流水线窗口大小；0 = stop-and-wait
	GitName                       *string            `json:"gitName,omitempty" form:"gitName"`                                             // Git author name // Git 提交的作者名称
	GitEmail                      *string            `json:"gitEmail,omitempty" form:"gitEmail"`                                           // Git author email // Git 提交的作者邮箱
	GitAllowedPrivateHosts        *[]string          `json:"gitAllowedPrivateHosts,omitempty" form:"gitAllowedPrivateHosts"`               // Allowed private Git hosts // 允许访问的私网 Git 主机
}

// AdminUserDatabaseConfig User database configuration structure
// AdminUserDatabaseConfig 用户数据库配置结构
type AdminUserDatabaseConfig struct {
	Type                string `json:"type" form:"type" binding:"omitempty,oneof=mysql postgres sqlite"` // Database type (mysql, postgres, sqlite) // 数据库类型
	Path                string `json:"path" form:"path"`                                                 // SQLite database file path // SQLite 数据库文件路径
	UserName            string `json:"userName" form:"userName"`                                         // Username // 用户名
	Password            string `json:"password" form:"password"`                                         // Password // 密码
	Host                string `json:"host" form:"host"`                                                 // Host // 主机
	Port                int    `json:"port" form:"port"`                                                 // Port // 端口
	Name                string `json:"name" form:"name"`                                                 // Database name // 数据库名
	SSLMode             string `json:"sslMode" form:"sslMode"`                                           // SSL mode (postgres only) // SSL 模式
	Schema              string `json:"schema" form:"schema"`                                             // Database schema (postgres only) // 数据库 Schema
	MaxIdleConns        int    `json:"maxIdleConns" form:"maxIdleConns"`                                 // Max idle connections // 最大闲置连接数
	MaxOpenConns        int    `json:"maxOpenConns" form:"maxOpenConns"`                                 // Max open connections // 最大打开连接数
	ConnMaxLifetime     string `json:"connMaxLifetime" form:"connMaxLifetime"`                           // Connection max lifetime // 连接最大生命周期
	ConnMaxIdleTime     string `json:"connMaxIdleTime" form:"connMaxIdleTime"`                           // Connection max idle time // 空闲连接最大生命周期
	MaxWriteConcurrency int    `json:"maxWriteConcurrency" form:"maxWriteConcurrency"`                   // Max write concurrency // 最大并发写入数
	Charset             string `json:"charset" form:"charset"`                                           // Charset // 字符集
	ParseTime           bool   `json:"parseTime" form:"parseTime"`                                       // Parse time // 是否解析时间
}



// AdminCloudflareConfig Cloudflare tunnel configuration
// AdminCloudflareConfig Cloudflare 隧道配置
type AdminCloudflareConfig struct {
	Enabled    bool   `json:"enabled" form:"enabled"`       // Whether to enable cloudflare tunnel // 是否启用 cloudflare 隧道
	Token      string `json:"token" form:"token"`           // cloudflare tunnel token // cloudflare 隧道令牌
	LogEnabled bool   `json:"logEnabled" form:"logEnabled"` // Whether to enable cloudflare tunnel logging // 是否开启 cloudflare 隧道日志
}

// AdminSystemInfo system information response structure
// AdminSystemInfo 系统信息响应结构
type AdminSystemInfo struct {
	StartTime     time.Time        `json:"startTime"`     // Start time // 启动时间
	Uptime        float64          `json:"uptime"`        // Uptime (seconds) // 运行时间（秒）
	RuntimeStatus AdminRuntimeInfo `json:"runtimeStatus"` // Go runtime status // Go 运行时状态
	CPU           AdminCPUInfo     `json:"cpu"`           // CPU information // CPU 信息
	Memory        AdminMemoryInfo  `json:"memory"`        // Memory information // 内存信息
	Host          AdminHostInfo    `json:"host"`          // Host information // 主机信息
	Process       AdminProcessInfo `json:"process"`       // Process information // 进程信息
}

// AdminCPUInfo CPU information
// AdminCPUInfo CPU 信息
type AdminCPUInfo struct {
	ModelName     string         `json:"modelName"`     // Model name // 型号
	PhysicalCores int            `json:"physicalCores"` // Physical cores // 物理核心数
	LogicalCores  int            `json:"logicalCores"`  // Logical cores // 逻辑核心数
	Percent       []float64      `json:"percent"`       // Usage percentage per core // 每个核心的使用率
	LoadAvg       *AdminLoadInfo `json:"loadAvg"`       // Load average // 平均负载
}

// AdminLoadInfo system load information
// AdminLoadInfo 系统负载信息
type AdminLoadInfo struct {
	Load1  float64 `json:"load1"`  // Load 1 min // 1分钟负载
	Load5  float64 `json:"load5"`  // Load 5 min // 5分钟负载
	Load15 float64 `json:"load15"` // Load 15 min // 15分钟负载
}

// AdminMemoryInfo memory information
// AdminMemoryInfo 内存信息
type AdminMemoryInfo struct {
	Total           uint64  `json:"total"`           // Total physical memory // 系统总内存
	Available       uint64  `json:"available"`       // Available memory // 可用内存
	Used            uint64  `json:"used"`            // Used memory // 已用内存
	UsedPercent     float64 `json:"usedPercent"`     // Memory usage percentage // 内存使用率
	SwapTotal       uint64  `json:"swapTotal"`       // Total swap space // 交换区总量
	SwapUsed        uint64  `json:"swapUsed"`        // Used swap space // 交换区已用
	SwapUsedPercent float64 `json:"swapUsedPercent"` // Swap usage percentage // 交换区使用率
}

// AdminHostInfo host identification information
// AdminHostInfo 主机标识信息
type AdminHostInfo struct {
	Hostname       string    `json:"hostname"`       // Hostname // 主机名
	OS             string    `json:"os"`             // Operating system // 操作系统
	OSPretty       string    `json:"osPretty"`       // Detailed OS name // 详细操作系统名称
	Platform       string    `json:"platform"`       // Platform name // 平台
	Arch           string    `json:"arch"`           // Architecture // 架构
	KernelVersion  string    `json:"kernelVersion"`  // Kernel version // 内核版本
	Uptime         uint64    `json:"uptime"`         // System uptime // 系统运行时间
	CurrentTime    time.Time `json:"currentTime"`    // Current system time // 当前系统时间
	TimeZone       string    `json:"timezone"`       // Time zone name // 时区名称
	TimeZoneOffset int       `json:"timezoneOffset"` // Time zone offset in seconds // 时区偏移（秒）
}

// AdminProcessInfo current process information
// AdminProcessInfo 当前进程信息
type AdminProcessInfo struct {
	PID           int32   `json:"pid"`           // Process ID // 进程 ID
	PPID          int32   `json:"ppid"`          // Parent Process ID // 父进程 ID
	Name          string  `json:"name"`          // Process Name // 进程名称
	CPUPercent    float64 `json:"cpuPercent"`    // CPU Usage percentage // CPU 使用率
	MemoryPercent float32 `json:"memoryPercent"` // Memory Usage percentage // 内存使用率
}

// AdminRuntimeInfo Go runtime information
// AdminRuntimeInfo Go 运行时信息
type AdminRuntimeInfo struct {
	NumGoroutine int    `json:"numGoroutine"` // Number of goroutines // Goroutine 数量
	MemAlloc     uint64 `json:"memAlloc"`     // Allocated memory (bytes) // 已分配内存（字节）
	MemTotal     uint64 `json:"memTotal"`     // Total memory allocated (bytes) // 累计分配内存（字节）
	MemSys       uint64 `json:"memSys"`       // Memory obtained from system (bytes) // 从系统获取的内存（字节）
	HeapSys      uint64 `json:"heapSys"`      // Memory obtained from system for heap (bytes) // 堆占用的系统内存
	HeapIdle     uint64 `json:"heapIdle"`     // Memory in idle spans (bytes) // 空闲 Span 占用的内存
	HeapInuse    uint64 `json:"heapInuse"`    // Memory in in-use spans (bytes) // 正在使用的 Span 占用的内存
	HeapReleased uint64 `json:"heapReleased"` // Memory released to OS (bytes) // 释放回操作系统的内存（字节）
	StackSys     uint64 `json:"stackSys"`     // Memory obtained from system for stack (bytes) // 栈占用的系统内存
	MSpanSys     uint64 `json:"mSpanSys"`     // Memory obtained from system for mspan (bytes) // mspan 占用的系统内存
	MCacheSys    uint64 `json:"mCacheSys"`    // Memory obtained from system for mcache (bytes) // mcache 占用的系统内存
	BuckHashSys  uint64 `json:"buckHashSys"`  // Memory obtained from system for profiling bucket hash table (bytes) // 分析桶哈希表占用的系统内存
	GCSys        uint64 `json:"gcSys"`        // Memory obtained from system for metadata for GC (bytes) // GC 元数据占用的系统内存
	OtherSys     uint64 `json:"otherSys"`     // Other system memory (bytes) // 其他系统内存
	NextGC       uint64 `json:"nextGc"`       // Target heap size for the next GC cycle // 下次 GC 的目标堆大小
	NumGC        uint32 `json:"numGc"`        // Number of completed GC cycles // GC 次数
}

// ShareBaseURLUpdateRequest request body for updating the runtime share base URL.
// An empty baseUrl clears the override and falls back to server.share-base-url.
// ShareBaseURLUpdateRequest 运行时分享基址更新请求。baseUrl 为空表示清除
// 覆盖值，回落到 server.share-base-url。
type ShareBaseURLUpdateRequest struct {
	BaseURL string `json:"baseUrl" form:"baseUrl"` // New public base URL, http(s) only; empty clears the override // 新的公开基址，仅支持 http(s)；空值清除覆盖
}

// ShareBaseURLResponse current effective share base URL and its source.
// ShareBaseURLResponse 当前生效的分享基址及其来源。
type ShareBaseURLResponse struct {
	BaseURL string `json:"baseUrl"` // Effective base URL (empty when unset) // 生效的基址（未设置时为空）
	Source  string `json:"source"`  // "override" (runtime) or "config" (server.share-base-url) // override（运行时）或 config（配置）
}

// TokenCapabilitiesResponse describes the effective capabilities of the
// authenticated token. It never contains secrets, passwords or the raw token.
// TokenCapabilitiesResponse 描述当前已认证令牌的有效能力，绝不包含密钥、密码
// 或原始令牌字符串。
type TokenCapabilitiesResponse struct {
	UID         int64                    `json:"uid"`         // User ID // 用户 ID
	IsAdmin     bool                     `json:"isAdmin"`     // Effective admin flag // 是否生效管理员
	TokenID     string                   `json:"tokenId"`     // Database token ID (string for plugin convenience) // 数据库令牌 ID（字符串，便于插件消费）
	Protocol    string                   `json:"protocol"`    // Protocol dimension from scope // scope 中的协议维度
	Restricted  bool                     `json:"restricted"`  // Vault-restricted or not full permissions // 是否限制库或非全权限
	Vaults      []string                 `json:"vaults"`      // Allowed vaults (empty = unrestricted) // 允许的库（空 = 不限制）
	Permissions TokenCapabilityFunctions `json:"permissions"` // Effective function permissions // 有效功能权限
	ReadOnly    bool                     `json:"readOnly"`    // True when neither note nor file write is allowed // 不允许笔记与附件写时为 true
}

// TokenCapabilityFunctions effective read/write permissions per resource.
// TokenCapabilityFunctions 各资源的有效读写权限。
type TokenCapabilityFunctions struct {
	NoteRead     bool `json:"noteRead"`     // note_r // 笔记读
	NoteWrite    bool `json:"noteWrite"`    // note_w // 笔记写
	FileRead     bool `json:"fileRead"`     // file_r // 附件读
	FileWrite    bool `json:"fileWrite"`    // file_w // 附件写
	SettingRead  bool `json:"settingRead"`  // config_r // 设置读
	SettingWrite bool `json:"settingWrite"` // config_w // 设置写
	VaultRead    bool `json:"vaultRead"`    // vault_r // 库读
	VaultWrite   bool `json:"vaultWrite"`   // vault_w // 库写
}

// CLIUpgradeResponse metadata of the latest FastNodeSync-CLI release, proxied
// and cached by the server so restricted intranet clients can upgrade without
// direct GitHub access.
// CLIUpgradeResponse 服务端代理并缓存的最新 FastNodeSync-CLI 版本元数据，
// 供受限内网客户端在无法直连 GitHub 时升级。
type CLIUpgradeResponse struct {
	Repository  string     `json:"repository"`  // Fixed upstream repository // 固定上游仓库
	Version     string     `json:"version"`     // Latest tag name // 最新 tag
	Name        string     `json:"name"`        // Release name // 发布名称
	PublishedAt string     `json:"publishedAt"` // Publish time (RFC3339) // 发布时间
	HTMLURL     string     `json:"htmlUrl"`     // Release page URL // 发布页地址
	Body        string     `json:"body"`        // Release notes // 发布说明
	Assets      []CLIAsset `json:"assets"`      // Release assets // 发布资源
	Prerelease  bool       `json:"prerelease"`  // Prerelease flag // 预发布标记
	Cached      bool       `json:"cached"`      // Whether served from the short-lived cache // 是否来自短时缓存
	CachedAt    string     `json:"cachedAt"`    // When the cached payload was fetched // 缓存抓取时间
	Stale       bool       `json:"stale"`       // Served from cache after an upstream failure // 上游失败时回退到缓存
}

// CLIAsset a downloadable CLI release asset.
// CLIAsset 可下载的 CLI 发布资源。
type CLIAsset struct {
	Name        string `json:"name"`        // File name // 文件名
	DownloadURL string `json:"downloadUrl"` // Download URL // 下载地址
	Size        int64  `json:"size"`        // Size in bytes // 字节大小
}
