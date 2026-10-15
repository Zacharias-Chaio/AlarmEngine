// main.go - 命令行入口：参数解析、NATS 连接、发送主循环、信号处理与收尾统计。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"alarmengine/internal/natsclient"

	"github.com/nats-io/nats.go"
)

// config 汇总全部命令行参数。
type config struct {
	url      string
	subject  string
	module   string
	instance string
	scenario string
	duration time.Duration
	interval time.Duration
	jitter   float64
	burst    int
	hold     time.Duration
	report   time.Duration
	flush    time.Duration
	seed     int64
	devices  int
	verbose  bool
	dryRun   bool
	logFile  string
	list     bool
}

func main() {
	cfg, active, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "[FAIL] %v\n", err)
		os.Exit(2)
	}
	if cfg.list {
		printScenarios()
		return
	}

	rng := newRng(cfg.seed)
	simulator := newSim(cfg, rng)
	start := time.Now()
	stats := newStats(start)

	var closing atomic.Bool // 主动收尾时不提示断连告警
	conn, err := dial(cfg, &closing)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[FAIL] %v\n", err)
		os.Exit(1)
	}
	if conn != nil {
		defer conn.Close()
	}

	var logger *sendLogger
	if cfg.logFile != "" {
		if logger, err = newSendLogger(cfg.logFile); err != nil {
			fmt.Fprintf(os.Stderr, "[FAIL] 打开发送日志失败: %v\n", err)
			os.Exit(1)
		}
		defer logger.close()
	}

	printBanner(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var deadline time.Time
	if cfg.duration > 0 {
		deadline = start.Add(cfg.duration)
	}

	tick := time.NewTimer(nextDelay(cfg, rng))
	defer tick.Stop()
	reportTicker := time.NewTicker(cfg.report)
	defer reportTicker.Stop()
	flushTicker := time.NewTicker(cfg.flush)
	defer flushTicker.Stop()

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case at := <-tick.C:
			if !deadline.IsZero() && !at.Before(deadline) {
				break loop
			}
			for _, f := range simulator.step(at, active) {
				if cfg.verbose || cfg.dryRun {
					prefix := "[MSG]"
					if cfg.dryRun {
						prefix = "[DRY]"
					}
					fmt.Printf("%s %s %s\n", prefix, at.Format("15:04:05.000"), describe(f))
				}
				if !cfg.dryRun {
					if err := publish(conn, f); err != nil {
						stats.recordError()
						fmt.Printf("[WARN] %s 发布失败: %v\n", at.Format("15:04:05"), err)
						continue
					}
				}
				stats.record(f)
				simulator.remember(f)
				if logger != nil {
					logger.log(f, at)
				}
			}
			tick.Reset(nextDelay(cfg, rng))
		case at := <-reportTicker.C:
			fmt.Println(stats.report(at))
		case at := <-flushTicker.C:
			if conn != nil {
				if err := conn.Flush(); err != nil {
					fmt.Printf("[WARN] %s NATS Flush 失败: %v\n", at.Format("15:04:05"), err)
				}
			}
		}
	}

	end := time.Now()
	fmt.Println(stats.summary(end))
	if pending := simulator.pendingCount(); pending > 0 {
		fmt.Printf("[DONE] 未到期恢复 %d 条：对应告警在引擎侧保持活动状态\n", pending)
	}
	if conn != nil {
		closing.Store(true) // 主动收尾，抑制断连提示
		if err := conn.Drain(); err != nil {
			fmt.Printf("[WARN] NATS Drain 失败: %v\n", err)
		}
	}
	fmt.Println("[DONE] 已退出")
}

// parseFlags 解析并校验参数，返回生效的工况列表。
func parseFlags(args []string) (config, []scenario, error) {
	fs := flag.NewFlagSet("alarm-sender", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cfg config
	fs.StringVar(&cfg.url, "url", "nats://127.0.0.1:4222", "NATS 服务地址")
	fs.StringVar(&cfg.subject, "subject", "powerpulse.alarm.thingsmodel", "目标主题（powerpulse.alarm.{module}）")
	fs.StringVar(&cfg.module, "module", "", "source.module，默认取主题二级命名空间")
	fs.StringVar(&cfg.instance, "instance", "", "source.instance，默认与 module 相同")
	fs.StringVar(&cfg.scenario, "scenario", "normal", "工况，逗号分隔组合；list 查看全部")
	fs.DurationVar(&cfg.duration, "duration", 0, "运行时长（如 30m、24h），0 表示持续运行直到 Ctrl+C")
	fs.DurationVar(&cfg.interval, "interval", time.Second, "发送节拍基础间隔")
	fs.Float64Var(&cfg.jitter, "jitter", 0.2, "节拍随机抖动比例（0~1）")
	fs.IntVar(&cfg.burst, "burst", 100, "storm 工况每轮突发条数")
	fs.DurationVar(&cfg.hold, "hold", 30*time.Second, "设备告警触发到恢复的基准持续时间")
	fs.DurationVar(&cfg.report, "report", 30*time.Second, "统计报告间隔")
	fs.DurationVar(&cfg.flush, "flush", 5*time.Second, "NATS Flush 间隔（探测发送故障）")
	fs.Int64Var(&cfg.seed, "seed", 0, "随机种子（0 表示按当前时间，固定种子可复现）")
	fs.IntVar(&cfg.devices, "devices", 8, "模拟设备数量")
	fs.BoolVar(&cfg.verbose, "v", false, "打印每条发送消息")
	fs.BoolVar(&cfg.dryRun, "dry-run", false, "仅构造并打印消息，不连接 NATS")
	fs.StringVar(&cfg.logFile, "log", "", "发送日志文件（JSON Lines，供与引擎侧对账）")
	fs.BoolVar(&cfg.list, "list", false, "列出全部工况说明")
	if err := fs.Parse(args); err != nil {
		return cfg, nil, err
	}

	var names []string
	for _, part := range strings.Split(cfg.scenario, ",") {
		if part = strings.TrimSpace(part); part != "" {
			names = append(names, part)
		}
	}
	if len(names) == 0 {
		return cfg, nil, fmt.Errorf("-scenario 不能为空（使用 -list 查看可用工况）")
	}
	active := make([]scenario, 0, len(names))
	for _, name := range names {
		if name == "list" {
			cfg.list = true
			return cfg, nil, nil
		}
		sc, ok := findScenario(name)
		if !ok {
			return cfg, nil, fmt.Errorf("未知工况 %q（使用 -list 查看可用工况）", name)
		}
		if name == "mixed" && len(names) > 1 {
			return cfg, nil, fmt.Errorf("mixed 为全工况加权混合，不能与其他工况组合")
		}
		active = append(active, sc)
	}
	cfg.scenario = strings.Join(names, ",")

	if strings.TrimSpace(cfg.subject) == "" || strings.ContainsAny(cfg.subject, " \t\r\n") {
		return cfg, nil, fmt.Errorf("-subject 不能为空且不能包含空白字符")
	}
	if cfg.module == "" {
		derived := defaultModule(cfg.subject)
		if derived == "" {
			return cfg, nil, fmt.Errorf("无法从主题 %s 推导 source.module，请用 -module 显式指定", cfg.subject)
		}
		cfg.module = derived
	}
	if cfg.instance == "" {
		cfg.instance = cfg.module
	}
	if cfg.interval <= 0 {
		return cfg, nil, fmt.Errorf("-interval 必须大于 0")
	}
	if cfg.jitter < 0 || cfg.jitter > 1 {
		return cfg, nil, fmt.Errorf("-jitter 取值范围为 0~1")
	}
	if cfg.burst < 1 {
		return cfg, nil, fmt.Errorf("-burst 必须大于 0")
	}
	if cfg.devices < 1 {
		return cfg, nil, fmt.Errorf("-devices 必须大于 0")
	}
	if cfg.report <= 0 || cfg.flush <= 0 {
		return cfg, nil, fmt.Errorf("-report 与 -flush 必须大于 0")
	}
	if derived := defaultModule(cfg.subject); cfg.module != derived {
		fmt.Fprintf(os.Stderr, "[WARN] source.module（%s）与主题二级命名空间（%s）不一致，引擎侧将记录告警\n", cfg.module, derived)
	}
	if cfg.seed == 0 {
		cfg.seed = time.Now().UnixNano()
	}
	return cfg, active, nil
}

// defaultModule 从 powerpulse.alarm.{module}.... 主题提取模块名。
func defaultModule(subject string) string {
	const namespace = "powerpulse.alarm."
	if !strings.HasPrefix(subject, namespace) {
		return ""
	}
	rest := subject[len(namespace):]
	if i := strings.IndexByte(rest, '.'); i > 0 {
		return rest[:i]
	}
	return rest
}

// newRng 固定种子可复现，种子 0 按当前时间。
func newRng(seed int64) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(seed), 0x9E3779B97F4A7C15))
}

// dial 连接 NATS：长期运行依赖自动重连；dry-run 返回 nil 连接。
func dial(cfg config, closing *atomic.Bool) (*nats.Conn, error) {
	if cfg.dryRun {
		return nil, nil
	}
	conn, err := nats.Connect(cfg.url,
		nats.Name("alarm-sender"),
		nats.Timeout(3*time.Second),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if closing.Load() {
				return // 主动收尾触发的断开，不算异常
			}
			fmt.Printf("[WARN] %s NATS 连接断开: %v\n", time.Now().Format("15:04:05"), err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			fmt.Printf("[INFO] %s NATS 已重连: %s\n", time.Now().Format("15:04:05"), c.ConnectedUrl())
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("连接 NATS 失败（%s）: %w", cfg.url, err)
	}
	return conn, nil
}

// publish 按标准信封头发布一条消息。
func publish(conn *nats.Conn, f frame) error {
	message := nats.NewMsg(f.subject)
	if message.Header == nil {
		message.Header = make(nats.Header)
	}
	for key, value := range f.headers {
		message.Header.Set(key, value)
	}
	message.Data = f.payload
	return conn.PublishMsg(message)
}

// nextDelay 计算下一个节拍：基础间隔 ± 抖动比例。
func nextDelay(cfg config, rng *rand.Rand) time.Duration {
	delay := float64(cfg.interval)
	if cfg.jitter > 0 {
		delay *= 1 + cfg.jitter*(2*rng.Float64()-1)
	}
	if delay < float64(10*time.Millisecond) {
		delay = float64(10 * time.Millisecond)
	}
	return time.Duration(delay)
}

func printBanner(cfg config) {
	fmt.Println("[INIT] PowerPulse 标准告警模拟发送工具（alarm-sender）")
	fmt.Printf("[INIT] 总线 %s | 主题 %s | source %s/%s\n", cfg.url, cfg.subject, cfg.module, cfg.instance)
	duration := "持续运行（Ctrl+C 停止）"
	if cfg.duration > 0 {
		duration = cfg.duration.String()
	}
	fmt.Printf("[INIT] 工况 %s | 节拍 %s（抖动 %.0f%%）| 时长 %s | 随机种子 %d\n",
		cfg.scenario, cfg.interval, cfg.jitter*100, duration, cfg.seed)
	if cfg.dryRun {
		fmt.Println("[INIT] dry-run：仅构造并打印消息，不连接 NATS")
	}
	if cfg.logFile != "" {
		fmt.Printf("[INIT] 发送日志：%s\n", cfg.logFile)
	}
}

func printScenarios() {
	fmt.Println("可用工况（-scenario 逗号分隔可组合；mixed 不可与其他组合）：")
	for _, sc := range scenarios {
		fmt.Printf("  %-14s %s\n", sc.name, sc.desc)
	}
	fmt.Println("示例：go run ./tools/alarm-sender -scenario device,software -duration 10m")
}

// sendLogger 发送日志（JSON Lines）：每条一行，供与引擎侧数据对账。
type sendLogger struct {
	file *os.File
}

type sendLogEntry struct {
	SentAt   time.Time `json:"sent_at"`
	Subject  string    `json:"subject"`
	AlarmID  string    `json:"alarm_id"`
	Scenario string    `json:"scenario"`
	Payload  string    `json:"payload"`
}

func newSendLogger(path string) (*sendLogger, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &sendLogger{file: file}, nil
}

func (l *sendLogger) log(f frame, at time.Time) {
	entry := sendLogEntry{
		SentAt:   at,
		Subject:  f.subject,
		AlarmID:  f.headers[natsclient.HeaderMessageID],
		Scenario: f.tag,
		Payload:  string(f.payload),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	l.file.Write(append(data, '\n'))
}

func (l *sendLogger) close() {
	l.file.Close()
}
