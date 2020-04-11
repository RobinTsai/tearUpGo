// base 包字义了通用的 go 命令，特别是 logging 和 Command 结构体
package base

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/scanner"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"cmd/go/internal/cfg"
	"cmd/go/internal/str"
)

// Command 是一个 go 命令的实现，比如 go build、go fix
type Command struct {
	// 运行命令
	Run func(cmd *Command, args []string) // Run 是一个 func 类型

	// 单行显示的使用信息
	// The words between "go" and the first flag or argument in the line are taken to be the command name.
	UsageLine string

	// Short 是使用 go help 时输出的短信息
	Short string

	// Long 是 go help <this-command> 输出的长信息
	Long string

	// Flag is a set of flags specific to this command.
	Flag flag.FlagSet

	// CustomFlags indicates that the command will do its own
	// flag parsing.
	CustomFlags bool

	// Commands lists the available commands and help topics.
	// The order here is the order in which they are printed by 'go help'.
	// Note that subcommands are in general best avoided.
	Commands []*Command
}

var Go = &Command{
	UsageLine: "go",
	Long:      `Go is a tool for managing Go source code.`,
	// Commands initialized in package main
}

// LongName 返回了命令的长名称：介于 go 和参数间的所有的单词
func (c *Command) LongName() string {
	name := c.UsageLine
	if i := strings.Index(name, " ["); i >= 0 {
		name = name[:i] // name 取 ` [` 前的所有字符
	}
	if name == "go" {
		return "" // 如果命令是 go 就没 LongName
	}
	return strings.TrimPrefix(name, "go ") // 省略开头的 `go ` 信息
}

// Name 返回命令的短名称：就是参数前的最后一个单词
func (c *Command) Name() string {
	name := c.LongName() // 取到 LongName（`go ` 与第一个参数之间的单词）
	if i := strings.LastIndex(name, " "); i >= 0 { // 取最后一个空格的下标
		name = name[i+1:]  // 取最后一个空格下标后面的所有字母（最后一个单词）
	}
	return name
}

func (c *Command) Usage() {
	fmt.Fprintf(os.Stderr, "usage: %s\n", c.UsageLine)
	fmt.Fprintf(os.Stderr, "Run 'go help %s' for details.\n", c.LongName())
	SetExitStatus(2)
	Exit()
}

// 判断一个命令是不是可以执行的；如果不是，那它就是一个伪命令，如 importpath
func (c *Command) Runnable() bool {
	return c.Run != nil // 看上面 Run 是一个 func 类型
}

// 定义了一个 func 数组，看样子是在退出的时候去挨个执行的
// 这个是不是就是 defer 字义的函数啊？
var atExitFuncs []func()

func AtExit(f func()) { // 加入 func()
	atExitFuncs = append(atExitFuncs, f)
}

// 退出函数会先执行 atExitFuncs 中的 func
// 然后调 os.Exit() 终止程序
func Exit() {
	for _, f := range atExitFuncs {
		f() // 退出时挨个执行 func
	}
	os.Exit(exitStatus) // 定义在 src/os/proc.go 中，退出时有一个退出码，0 表示 success
	// os.Exit 函数执行时，程序立刻结束，定义的 defer 函数将不再执行
}

func Fatalf(format string, args ...interface{}) {
	Errorf(format, args...)
	Exit()
}

func Errorf(format string, args ...interface{}) {
	log.Printf(format, args...)
	SetExitStatus(1)
}

func ExitIfErrors() {
	if exitStatus != 0 {
		Exit()
	}
}

// exitStatus 是一个状态值，退出码。
// 0 为 success，非 0 为异常
// 它的更新是上锁的
var exitStatus = 0
var exitMu sync.Mutex

// SetExitStatus 只能 set 比原状态大的值，这是为什么？
func SetExitStatus(n int) {
	exitMu.Lock()
	if exitStatus < n {
		exitStatus = n
	}
	exitMu.Unlock()
}

func GetExitStatus() int {
	return exitStatus
}

// Run runs the command, with stdout and stderr
// connected to the go command's own stdout and stderr.
// If the command fails, Run reports the error using Errorf.
// 执行命令
func Run(cmdargs ...interface{}) {
	cmdline := str.StringList(cmdargs...) // 获取参数列表，每个参数必须是 []string 或 string
	if cfg.BuildN || cfg.BuildX { // cfg 是包，一些环境配置信息。这两个是 -n 和 -x 参数，是用在 build 等的 flag
		fmt.Printf("%s\n", strings.Join(cmdline, " "))　
		if cfg.BuildN { // 若指定了 -n
			return
		}
	}

	// exec.Command 返回了要执行命令的结构体（Path 和 Args）
	// Path 是可执行文件的路径和可执行文件本身
	cmd := exec.Command(cmdline[0], cmdline[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// 执行命令。
	// 这个函数中包含了 Start 和 Wait 两个部分，是很基层的调用，后面可以看一下
	if err := cmd.Run(); err != nil {
		Errorf("%v", err)
	}
}

// RunStdin 只是比 Run 多了个与 Stdin 的连接
func RunStdin(cmdline []string) {
	cmd := exec.Command(cmdline[0], cmdline[1:]...)
	cmd.Stdin = os.Stdin // RunStdin 设置了 Stdin，连接到了用户输入
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = cfg.OrigEnv
	StartSigHandlers() // 它会开一个协程，等待一个信号（os.Signal）发生
	if err := cmd.Run(); err != nil {
		Errorf("%v", err)
	}
}

// Usage is the usage-reporting function, filled in by package main
// but here for reference by other packages.
var Usage func()

// ExpandScanner expands a scanner.List error into all the errors in the list.
// The default Error method only shows the first error
// and does not shorten paths.
func ExpandScanner(err error) error {
	// Look for parser errors.
	if err, ok := err.(scanner.ErrorList); ok {
		// Prepare error with \n before each message.
		// When printed in something like context: %v
		// this will put the leading file positions each on
		// its own line. It will also show all the errors
		// instead of just the first, as err.Error does.
		var buf bytes.Buffer
		for _, e := range err {
			e.Pos.Filename = ShortPath(e.Pos.Filename)
			buf.WriteString("\n")
			buf.WriteString(e.Error())
		}
		return errors.New(buf.String())
	}
	return err
}
