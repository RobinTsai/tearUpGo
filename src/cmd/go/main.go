//go:generate ./mkalldocs.sh

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"cmd/go/internal/base"
	"cmd/go/internal/bug"
	"cmd/go/internal/cfg"
	"cmd/go/internal/clean"
	"cmd/go/internal/doc"
	"cmd/go/internal/envcmd"
	"cmd/go/internal/fix"
	"cmd/go/internal/fmtcmd"
	"cmd/go/internal/generate"
	"cmd/go/internal/get"
	"cmd/go/internal/help"
	"cmd/go/internal/list"
	"cmd/go/internal/modcmd"
	"cmd/go/internal/modfetch"
	"cmd/go/internal/modget"
	"cmd/go/internal/modload"
	"cmd/go/internal/run"
	"cmd/go/internal/test"
	"cmd/go/internal/tool"
	"cmd/go/internal/version"
	"cmd/go/internal/vet"
	"cmd/go/internal/work"
)

func init() {
	base.Go.Commands = []*base.Command{
		bug.CmdBug,
		work.CmdBuild,
		clean.CmdClean,
		doc.CmdDoc,
		envcmd.CmdEnv,
		fix.CmdFix,
		fmtcmd.CmdFmt,
		generate.CmdGenerate,
		modget.CmdGet,
		work.CmdInstall,
		list.CmdList,
		modcmd.CmdMod,
		run.CmdRun,
		test.CmdTest,
		tool.CmdTool,
		version.CmdVersion,
		vet.CmdVet,

		help.HelpBuildmode,
		help.HelpC,
		help.HelpCache,
		help.HelpEnvironment,
		help.HelpFileType,
		modload.HelpGoMod,
		help.HelpGopath,
		get.HelpGopathGet,
		modfetch.HelpGoproxy,
		help.HelpImportPath,
		modload.HelpModules,
		modget.HelpModuleGet,
		modfetch.HelpModuleAuth,
		modfetch.HelpModulePrivate,
		help.HelpPackages,
		test.HelpTestflag,
		test.HelpTestfunc,
	}
}

func main() {
	_ = go11tag             // 是一个 bool 的 true
	flag.Usage = base.Usage // 是一个空的 func() 值
	flag.Parse()            // 可以去读一下 flag 包，它解析了传入的 flags（command.Flag）
	log.SetFlags(0)         // 设置 log 包中的 flag 为 0（未知）

	args := flag.Args()
	if len(args) < 1 {
		base.Usage()
	}

	if args[0] == "get" || args[0] == "help" {
		// true:（1. 配置 2. go mod init 3.）
		// false: 1. auto 模式但在此目录及根目录中找不到 mod
		if !modload.WillBeEnabled() { // 去看 mod 是不是开启或将要开启
			// Replace module-aware get with GOPATH get if appropriate.
			*modget.CmdGet = *get.CmdGet
		}
	}

	cfg.CmdName = args[0] // for error messages
	if args[0] == "help" {
		help.Help(os.Stdout, args[1:]) // 生成 help 文档，这个得看一下
		return
	}

	// Diagnose common mistake: GOPATH==GOROOT.
	if gopath := cfg.BuildContext.GOPATH; filepath.Clean(gopath) == filepath.Clean(runtime.GOROOT()) {
		fmt.Fprintf(os.Stderr, "warning: GOPATH set to GOROOT (%s) has no effect\n", gopath)
	} else {
		for _, p := range filepath.SplitList(gopath) {
			// Some GOPATHs have empty directory elements - ignore them.
			// See issue 21928 for details.
			if p == "" {
				continue
			}
			// GOPATH 不可以定义为 ~ 开头的路径（设置不成功，可能是配置文件等方式）
			if strings.HasPrefix(p, "~") {
				fmt.Fprintf(os.Stderr, "go: GOPATH entry cannot start with shell metacharacter '~': %q\n", p)
				os.Exit(2)
			}
			if !filepath.IsAbs(p) { // 不是绝对路径时
				if cfg.Getenv("GOPATH") == "" {
					// We inferred $GOPATH from $HOME and did a bad job at it.
					// Instead of dying, uninfer it.
					cfg.BuildContext.GOPATH = ""
				} else { // GOPATH 不可以是相对路径
					fmt.Fprintf(os.Stderr, "go: GOPATH entry is relative; must be absolute path: %q.\nFor more details see: 'go help gopath'\n", p)
					os.Exit(2)
				}
			}
		}
	}

	if fi, err := os.Stat(cfg.GOROOT); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "go: cannot find GOROOT directory: %v\n", cfg.GOROOT)
		os.Exit(2)
	}

	// Set environment (GOOS, GOARCH, etc) explicitly.
	// In theory all the commands we invoke should have
	// the same default computation of these as we do,
	// but in practice there might be skew
	// This makes sure we all agree.
	cfg.OrigEnv = os.Environ()
	cfg.CmdEnv = envcmd.MkEnv()
	for _, env := range cfg.CmdEnv {
		if os.Getenv(env.Name) != env.Value {
			os.Setenv(env.Name, env.Value)
		}
	}

BigCmdLoop:
	// go 的下一级算是 bigCom
	for bigCmd := base.Go; ; {
		for _, cmd := range bigCmd.Commands {
			if cmd.Name() != args[0] {
				continue // 不是指定的命令时跳过
			}
			// 以下找到了要执行的命令

			// 有嵌套子命令
			if len(cmd.Commands) > 0 {
				bigCmd = cmd
				args = args[1:]
				if len(args) == 0 { // 子命令后无参数，输出使用信息
					help.PrintUsage(os.Stderr, bigCmd)
					base.SetExitStatus(2)
					base.Exit()
				}
				if args[0] == "help" {
					// Accept 'go mod help' and 'go mod help foo' for 'go help mod' and 'go help mod foo'.
					help.Help(os.Stdout, append(strings.Split(cfg.CmdName, " "), args[1:]...))
					return
				}
				cfg.CmdName += " " + args[0]
				continue BigCmdLoop
			}
			if !cmd.Runnable() {
				continue
			}
			cmd.Flag.Usage = func() { cmd.Usage() }
			if cmd.CustomFlags {
				args = args[1:]
			} else {
				base.SetFromGOFLAGS(cmd.Flag)
				cmd.Flag.Parse(args[1:])
				args = cmd.Flag.Args()
			}
			// 执行命令
			cmd.Run(cmd, args)
			base.Exit()
			return
		}
		// 如果没找到命令
		helpArg := ""
		if i := strings.LastIndex(cfg.CmdName, " "); i >= 0 {
			helpArg = " " + cfg.CmdName[:i]
		}
		fmt.Fprintf(os.Stderr, "go %s: unknown command\nRun 'go help%s' for usage.\n", cfg.CmdName, helpArg)
		base.SetExitStatus(2)
		base.Exit()
	}
}

// 这里竟藏着另一个 init()，赋值了 base.Usage
func init() {
	base.Usage = mainUsage
}

func mainUsage() {
	help.PrintUsage(os.Stderr, base.Go)
	os.Exit(2) // 退出状态码为 2，可用 `echo $?` 查看
}
