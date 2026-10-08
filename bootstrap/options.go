package bootstrap

// Option 是 Bootstrap 的可选配置项(函数选项模式)。
//
// 用法:
//
//	h, err := bootstrap.BootstrapWithOptions(configPath,
//	    bootstrap.WithStaticDir("/opt/pb/ui"),
//	)
//
// 兼容性:Bootstrap(configPath) 等价于无选项的 BootstrapWithOptions,
// 既有调用方(server、fnos)不受影响。
type Option func(*Options)

// Options 控制 Bootstrap 的行为。零值字段取默认。
type Options struct {
	// StaticDir 是前端构建产物的根目录。
	// 该目录需包含 index.html 与 assets/(Vite 构建产物布局)。
	// 默认 "./static"(相对进程工作目录)。
	// 典型消费者:fnos 单容器部署时把前端内嵌到自定义路径。
	StaticDir string
}

func defaultOptions() Options {
	return Options{
		StaticDir: "./static",
	}
}

// WithStaticDir 覆盖前端静态资源目录(空串忽略,保持默认)。
func WithStaticDir(dir string) Option {
	return func(o *Options) {
		if dir != "" {
			o.StaticDir = dir
		}
	}
}

// applyOptions 应用选项到包级配置。
// 注:包级配置供 NoRoute/静态服务等闭包读取;多次 Bootstrap 以最后一次为准(进程内通常仅一次)。
func applyOptions(opts ...Option) Options {
	o := defaultOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}
