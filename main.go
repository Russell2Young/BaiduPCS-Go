package main

import (
	"encoding/hex"
	"fmt"
	"github.com/iikira/BaiduPCS-Go/baidupcs"
	"github.com/iikira/BaiduPCS-Go/internal/pcscommand"
	"github.com/iikira/BaiduPCS-Go/internal/pcsconfig"
	_ "github.com/iikira/BaiduPCS-Go/internal/pcsinit"
	"github.com/iikira/BaiduPCS-Go/internal/pcsupdate"
	"github.com/iikira/BaiduPCS-Go/internal/pcsweb"
	"github.com/iikira/BaiduPCS-Go/pcscache"
	"github.com/iikira/BaiduPCS-Go/pcsliner"
	"github.com/iikira/BaiduPCS-Go/pcspath"
	"github.com/iikira/BaiduPCS-Go/pcstable"
	"github.com/iikira/BaiduPCS-Go/pcsutil"
	"github.com/iikira/BaiduPCS-Go/pcsutil/checksum"
	"github.com/iikira/BaiduPCS-Go/pcsutil/converter"
	"github.com/iikira/BaiduPCS-Go/pcsutil/getip"
	"github.com/iikira/BaiduPCS-Go/pcsutil/pcstime"
	"github.com/iikira/BaiduPCS-Go/pcsverbose"
	"github.com/iikira/BaiduPCS-Go/requester"
	"github.com/iikira/args"
	"github.com/olekukonko/tablewriter"
	"github.com/peterh/liner"
	"github.com/urfave/cli"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	// Version 版本号
	Version = "v3.5.5-devel"

	historyFilePath = filepath.Join(pcsconfig.GetConfigDir(), "pcs_command_history.txt")
	reloadFn        = func(c *cli.Context) error {
		err := pcsconfig.Config.Reload()
		if err != nil {
			fmt.Printf("重载配置错误: %s\n", err)
		}
		return nil
	}
	saveFunc = func(c *cli.Context) error {
		err := pcsconfig.Config.Save()
		if err != nil {
			fmt.Printf("保存配置错误: %s\n", err)
		}
		return nil
	}

	cryptoDescription = `
	可用的方法 <method>:
		aes-128-ctr, aes-192-ctr, aes-256-ctr,
		aes-128-cfb, aes-192-cfb, aes-256-cfb,
		aes-128-ofb, aes-192-ofb, aes-256-ofb.

	密钥 <key>:
		aes-128 对应key长度为16, aes-192 对应key长度为24, aes-256 对应key长度为32,
		如果key长度不符合, 则自动修剪key, 舍弃超出长度的部分, 长度不足的部分用'\0'填充.

	GZIP <disable-gzip>:
		在文件加密之前, 启用GZIP压缩文件; 文件解密之后启用GZIP解压缩文件, 默认启用,
		如果不启用, 则无法检测文件是否解密成功, 解密文件时会保留源文件, 避免解密失败造成文件数据丢失.`

	isCli bool
)

func init() {
	pcsutil.ChWorkDir()

	err := pcsconfig.Config.Init()
	switch err {
	case nil:
	case pcsconfig.ErrConfigFileNoPermission, pcsconfig.ErrConfigContentsParseError:
		fmt.Fprintf(os.Stderr, "FATAL ERROR: config file error: %s\n", err)
		os.Exit(1)
	default:
		fmt.Printf("WARNING: config init error: %s\n", err)
	}

	// 启动缓存回收
	pcscache.DirCache.SetLifeTime(10 * time.Second)
	pcscache.DirCache.GC()
	requester.TCPAddrCache.GC()
}

func main() {
	defer pcsconfig.Config.Close()

	app := cli.NewApp()
	app.Name = "BaiduPCS-Go"
	app.Version = Version
	app.Author = "iikira/BaiduPCS-Go: https://github.com/iikira/BaiduPCS-Go"
	app.Copyright = "(c) 2016-2018 iikira."
	app.Usage = "百度网盘客户端 for " + runtime.GOOS + "/" + runtime.GOARCH
	app.Description = `BaiduPCS-Go 使用Go语言编写的百度网盘命令行客户端, 为操作百度网盘, 提供实用功能.
	具体功能, 参见 COMMANDS 列表

	特色:
		网盘内列出文件和目录, 支持通配符匹配路径;
		下载网盘内文件, 支持网盘内目录 (文件夹) 下载, 支持多个文件或目录下载, 支持断点续传和高并发高速下载.

	---------------------------------------------------
	前往 https://github.com/iikira/BaiduPCS-Go 以获取更多帮助信息!
	前往 https://github.com/iikira/BaiduPCS-Go/releases 以获取程序更新信息!
	---------------------------------------------------

	交流反馈:
		提交Issue: https://github.com/iikira/BaiduPCS-Go/issues
		邮箱: i@mail.iikira.com`

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:        "verbose",
			Usage:       "启用调试",
			EnvVar:      pcsverbose.EnvVerbose,
			Destination: &pcsverbose.IsVerbose,
		},
	}
	app.Action = func(c *cli.Context) {
		if c.NArg() != 0 {
			fmt.Printf("未找到命令: %s\n运行命令 %s help 获取帮助\n", c.Args().Get(0), app.Name)
			return
		}

		isCli = true
		pcsverbose.Verbosef("VERBOSE: 这是一条调试信息\n\n")

		var (
			line = pcsliner.NewLiner()
			err  error
		)

		line.History, err = pcsliner.NewLineHistory(historyFilePath)
		if err != nil {
			fmt.Printf("警告: 读取历史命令文件错误, %s\n", err)
		}

		line.ReadHistory()
		defer func() {
			line.DoWriteHistory()
			line.Close()
		}()

		// tab 自动补全命令
		line.State.SetCompleter(func(line string) (s []string) {
			var (
				lineArgs                   = args.GetArgs(line)
				numArgs                    = len(lineArgs)
				acceptCompleteFileCommands = []string{
					"cd", "cp", "download", "export", "locate", "ls", "meta", "mkdir", "mv", "rapidupload", "rm", "share", "tree", "upload",
				}
				closed = strings.LastIndex(line, " ") == len(line)-1
			)

			for _, cmd := range app.Commands {
				for _, name := range cmd.Names() {
					if !strings.HasPrefix(name, line) {
						continue
					}

					s = append(s, name+" ")
				}
			}

			switch numArgs {
			case 0:
				return
			case 1:
				if !closed {
					return
				}
			}

			thisCmd := app.Command(lineArgs[0])
			if thisCmd == nil {
				return
			}

			if !pcsutil.ContainsString(acceptCompleteFileCommands, thisCmd.FullName()) {
				return
			}

			var (
				activeUser = pcsconfig.Config.ActiveUser()
				pcs        = pcsconfig.Config.ActiveUserBaiduPCS()
				targetPath string
			)

			if !closed {
				targetPath = lineArgs[numArgs-1]
				pcspath.EscapeStrings(lineArgs[:numArgs-1]) // 转义
			} else {
				pcspath.EscapeStrings(lineArgs)
			}

			switch {
			case targetPath == "." || strings.HasSuffix(targetPath, "/."):
				s = append(s, line+"/")
				return
			case targetPath == ".." || strings.HasSuffix(targetPath, "/.."):
				s = append(s, line+"/")
				return
			}

			var (
				targetDir string
				isAbs     = path.IsAbs(targetPath)
				isDir     = strings.LastIndex(targetPath, "/") == len(targetPath)-1
			)

			if isAbs {
				targetDir = path.Dir(targetPath)
			} else {
				targetDir = path.Join(activeUser.Workdir, targetPath)
				if !isDir {
					targetDir = path.Dir(targetDir)
				}
			}
			filesPtr := pcscache.DirCache.Get(targetDir)

			if filesPtr == nil {
				files, err := pcs.FilesDirectoriesList(targetDir, baidupcs.DefaultOrderOptions)
				if err != nil {
					return
				}
				pcscache.DirCache.Set(targetDir, &files)
				filesPtr = &files
			}

			// fmt.Println("-", targetDir, targetPath, "-")

			for _, file := range *filesPtr {
				if file == nil {
					continue
				}

				var (
					appendLine string
				)

				// 已经有的情况
				if !closed {
					if !strings.HasPrefix(file.Path, path.Clean(path.Join(targetDir, path.Base(targetPath)))) {
						if path.Base(targetDir) == path.Base(targetPath) {
							appendLine = strings.Join(append(lineArgs[:numArgs-1], pcspath.Escape(path.Join(targetPath, file.Filename))), " ")
							goto handle
						}
						// fmt.Println(file.Path, targetDir, targetPath)
						continue
					}
					// fmt.Println(path.Clean(path.Join(path.Dir(targetPath), file.Filename)), targetPath, file.Filename)
					appendLine = strings.Join(append(lineArgs[:numArgs-1], pcspath.Escape(path.Clean(path.Join(path.Dir(targetPath), file.Filename)))), " ")
					goto handle
				}
				// 没有的情况
				appendLine = strings.Join(append(lineArgs, pcspath.Escape(file.Filename)), " ")
				goto handle

			handle:
				if file.Isdir {
					s = append(s, appendLine+"/")
					continue
				}
				s = append(s, appendLine+" ")
				continue
			}

			return
		})

		fmt.Printf("提示: 方向键上下可切换历史命令.\n")
		fmt.Printf("提示: Ctrl + A / E 跳转命令 首 / 尾.\n")
		fmt.Printf("提示: 输入 help 获取帮助.\n")

		for {
			var (
				prompt     string
				activeUser = pcsconfig.Config.ActiveUser()
			)

			if activeUser.Name != "" {
				// 格式: BaiduPCS-Go:<工作目录> <百度ID>$
				// 工作目录太长时, 会自动缩略
				prompt = app.Name + ":" + converter.ShortDisplay(path.Base(activeUser.Workdir), 20) + " " + activeUser.Name + "$ "
			} else {
				// BaiduPCS-Go >
				prompt = app.Name + " > "
			}

			commandLine, err := line.State.Prompt(prompt)
			switch err {
			case liner.ErrPromptAborted:
				return
			case nil:
				// continue
			default:
				fmt.Println(err)
				return
			}

			line.State.AppendHistory(commandLine)

			cmdArgs := args.GetArgs(commandLine)
			if len(cmdArgs) == 0 {
				continue
			}

			s := []string{os.Args[0]}
			s = append(s, cmdArgs...)

			// 恢复原始终端状态
			// 防止运行命令时程序被结束, 终端出现异常
			line.Pause()
			c.App.Run(s)
			line.Resume()
		}
	}

	app.Commands = []cli.Command{
		{
			Name:     "web",
			Usage:    "启用 web 客户端 (测试中)",
			Category: "其他",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				fmt.Printf("web 客户端功能为实验性功能, 测试中, 打开 http://localhost:%d 查看效果\n", c.Uint("port"))
				fmt.Println(pcsweb.StartServer(c.Uint("port")))
				return nil
			},
			Flags: []cli.Flag{
				cli.UintFlag{
					Name:  "port",
					Usage: "自定义端口",
					Value: 8080,
				},
			},
		},
		{
			Name:     "run",
			Usage:    "执行系统命令",
			Category: "其他",
			Action: func(c *cli.Context) error {
				if c.NArg() == 0 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				cmd := exec.Command(c.Args().First(), c.Args().Tail()...)
				cmd.Stdout = os.Stdout
				cmd.Stdin = os.Stdin
				cmd.Stderr = os.Stderr

				err := cmd.Run()
				if err != nil {
					fmt.Println(err)
				}

				return nil
			},
		},
		{
			Name:     "env",
			Usage:    "显示程序环境变量",
			Category: "其他",
			Action: func(c *cli.Context) error {
				envStr := "%s=\"%s\"\n"
				envVar, ok := os.LookupEnv(pcsverbose.EnvVerbose)
				if ok {
					fmt.Printf(envStr, pcsverbose.EnvVerbose, envVar)
				} else {
					fmt.Printf(envStr, pcsverbose.EnvVerbose, "0")
				}

				envVar, ok = os.LookupEnv(pcsconfig.EnvConfigDir)
				if ok {
					fmt.Printf(envStr, pcsconfig.EnvConfigDir, envVar)
				} else {
					fmt.Printf(envStr, pcsconfig.EnvConfigDir, pcsconfig.GetConfigDir())
				}

				return nil
			},
		},
		{
			Name:     "update",
			Usage:    "检测程序更新",
			Category: "其他",
			Action: func(c *cli.Context) error {
				if c.IsSet("y") {
					if !c.Bool("y") {
						return nil
					}
				}
				pcsupdate.CheckUpdate(app.Version, c.Bool("y"))
				return nil
			},
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "y",
					Usage: "确认更新",
				},
			},
		},
		{
			Name:  "login",
			Usage: "登录百度账号",
			Description: `
	示例:
		BaiduPCS-Go login
		BaiduPCS-Go login -username=liuhua
		BaiduPCS-Go login -bduss=123456789

	常规登录:
		按提示一步一步来即可.

	百度BDUSS获取方法:
		参考这篇 Wiki: https://github.com/iikira/BaiduPCS-Go/wiki/关于-获取百度-BDUSS
		或者百度搜索: 获取百度BDUSS`,
			Category: "百度帐号",
			Before:   reloadFn,
			After:    saveFunc,
			Action: func(c *cli.Context) error {
				var bduss, ptoken, stoken string
				if c.IsSet("bduss") {
					bduss = c.String("bduss")
				} else if c.NArg() == 0 {
					var err error
					bduss, ptoken, stoken, err = pcscommand.RunLogin(c.String("username"), c.String("password"))
					if err != nil {
						fmt.Println(err)
						return err
					}
				} else {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				baidu, err := pcsconfig.Config.SetupUserByBDUSS(bduss, ptoken, stoken)
				if err != nil {
					fmt.Println(err)
					return nil
				}

				fmt.Println("百度帐号登录成功:", baidu.Name)
				return nil
			},
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "username",
					Usage: "登录百度帐号的用户名(手机号/邮箱/用户名)",
				},
				cli.StringFlag{
					Name:  "password",
					Usage: "登录百度帐号的用户名的密码",
				},
				cli.StringFlag{
					Name:  "bduss",
					Usage: "使用百度 BDUSS 来登录百度帐号",
				},
			},
		},
		{
			Name:  "su",
			Usage: "切换百度帐号",
			Description: `
	切换已登录的百度帐号:
	如果运行该条命令没有提供参数, 程序将会列出所有的百度帐号, 供选择切换.

	示例:
	BaiduPCS-Go su
	BaiduPCS-Go su <uid or name>
`,
			Category: "百度帐号",
			Before:   reloadFn,
			After:    saveFunc,
			Action: func(c *cli.Context) error {
				if c.NArg() >= 2 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				numLogins := pcsconfig.Config.NumLogins()

				if numLogins == 0 {
					fmt.Printf("未设置任何百度帐号, 不能切换\n")
					return nil
				}

				var (
					inputData = c.Args().Get(0)
					uid       uint64
				)

				if c.NArg() == 1 {
					// 直接切换
					uid, _ = strconv.ParseUint(inputData, 10, 64)
				} else if c.NArg() == 0 {
					// 输出所有帐号供选择切换
					cli.HandleAction(app.Command("loglist").Action, c)

					// 提示输入 index
					var index string
					fmt.Printf("输入要切换帐号的 # 值 > ")
					_, err := fmt.Scanln(&index)
					if err != nil {
						return nil
					}

					if n, err := strconv.Atoi(index); err == nil && n >= 0 && n < numLogins {
						uid = pcsconfig.Config.BaiduUserList()[n].UID
					} else {
						fmt.Printf("切换用户失败, 请检查 # 值是否正确\n")
						return nil
					}
				} else {
					cli.ShowCommandHelp(c, c.Command.Name)
				}

				switchedUser, err := pcsconfig.Config.SwitchUser(&pcsconfig.BaiduBase{
					Name: inputData,
				})
				if err != nil {
					switchedUser, err = pcsconfig.Config.SwitchUser(&pcsconfig.BaiduBase{
						UID: uid,
					})
					if err != nil {
						fmt.Printf("切换用户失败, %s\n", err)
						return nil
					}
				}

				if err = pcsconfig.Config.Save(); err != nil {
					fmt.Printf("保存配置错误: %s\n", err)
					return nil
				}

				fmt.Printf("切换用户成功, %s\n", switchedUser.Name)
				return nil
			},
		},
		{
			Name:        "logout",
			Usage:       "退出百度帐号",
			Description: "退出当前登录的百度帐号",
			Category:    "百度帐号",
			Before:      reloadFn,
			After:       saveFunc,
			Action: func(c *cli.Context) error {
				if pcsconfig.Config.NumLogins() == 0 {
					fmt.Println("未设置任何百度帐号, 不能退出")
					return nil
				}

				var (
					confirm    string
					activeUser = pcsconfig.Config.ActiveUser()
				)

				if !c.Bool("y") {
					fmt.Printf("确认退出百度帐号: %s ? (y/n) > ", activeUser.Name)
					_, err := fmt.Scanln(&confirm)
					if err != nil || (confirm != "y" && confirm != "Y") {
						return err
					}
				}

				deletedUser, err := pcsconfig.Config.DeleteUser(&pcsconfig.BaiduBase{
					UID: activeUser.UID,
				})
				if err != nil {
					fmt.Printf("退出用户 %s, 失败, 错误: %s\n", activeUser.Name, err)
				}

				fmt.Printf("退出用户成功, %s\n", deletedUser.Name)
				return nil
			},
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "y",
					Usage: "确认退出帐号",
				},
			},
		},
		{
			Name:        "loglist",
			Usage:       "列出帐号列表",
			Description: "列出所有已登录的百度帐号",
			Category:    "百度帐号",
			Before:      reloadFn,
			Action: func(c *cli.Context) error {
				list := pcsconfig.Config.BaiduUserList()
				fmt.Println(list.String())
				return nil
			},
		},
		{
			Name:        "who",
			Usage:       "获取当前帐号",
			Description: "获取当前帐号的信息",
			Category:    "百度帐号",
			Before:      reloadFn,
			Action: func(c *cli.Context) error {
				activeUser := pcsconfig.Config.ActiveUser()
				fmt.Printf("当前帐号 uid: %d, 用户名: %s, 性别: %s, 年龄: %.1f\n", activeUser.UID, activeUser.Name, activeUser.Sex, activeUser.Age)
				return nil
			},
		},
		{
			Name:        "quota",
			Usage:       "获取网盘配额",
			Description: "获取网盘的总储存空间, 和已使用的储存空间",
			Category:    "百度网盘",
			Before:      reloadFn,
			Action: func(c *cli.Context) error {
				pcscommand.RunGetQuota()
				return nil
			},
		},
		{
			Name:     "cd",
			Category: "百度网盘",
			Usage:    "切换工作目录",
			Description: `
	BaiduPCS-Go cd <目录, 绝对路径或相对路径>

	示例:

	切换 /我的资源 工作目录:
	BaiduPCS-Go cd /我的资源

	切换上级目录:
	BaiduPCS-Go cd ..

	切换根目录:
	BaiduPCS-Go cd /

	切换 /我的资源 工作目录, 并自动列出 /我的资源 下的文件和目录
	BaiduPCS-Go cd -l 我的资源

	使用通配符:
	BaiduPCS-Go cd /我的*
`,
			Before: reloadFn,
			After:  saveFunc,
			Action: func(c *cli.Context) error {
				if c.NArg() == 0 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				pcscommand.RunChangeDirectory(c.Args().Get(0), c.Bool("l"))

				return nil
			},
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "l",
					Usage: "切换工作目录后自动列出工作目录下的文件和目录",
				},
			},
		},
		{
			Name:      "ls",
			Aliases:   []string{"l", "ll"},
			Usage:     "列出目录",
			UsageText: app.Name + " ls <目录>",
			Description: `
	列出当前工作目录内的文件和目录, 或指定目录内的文件和目录

	示例:

	列出 我的资源 内的文件和目录
	BaiduPCS-Go ls 我的资源

	绝对路径
	BaiduPCS-Go ls /我的资源

	降序排序
	BaiduPCS-Go ls -desc 我的资源

	按文件大小降序排序
	BaiduPCS-Go ls -size -desc 我的资源

	使用通配符
	BaiduPCS-Go ls /我的*
`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				orderOptions := &baidupcs.OrderOptions{}
				switch {
				case c.IsSet("asc"):
					orderOptions.Order = baidupcs.OrderAsc
				case c.IsSet("desc"):
					orderOptions.Order = baidupcs.OrderDesc
				default:
					orderOptions.Order = baidupcs.OrderAsc
				}

				switch {
				case c.IsSet("time"):
					orderOptions.By = baidupcs.OrderByTime
				case c.IsSet("name"):
					orderOptions.By = baidupcs.OrderByName
				case c.IsSet("size"):
					orderOptions.By = baidupcs.OrderBySize
				default:
					orderOptions.By = baidupcs.OrderByName
				}

				pcscommand.RunLs(c.Args().Get(0), &pcscommand.LsOptions{
					Total: c.Bool("l") || c.Parent().Args().Get(0) == "ll",
				}, orderOptions)

				return nil
			},
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "l",
					Usage: "详细显示",
				},
				cli.BoolFlag{
					Name:  "asc",
					Usage: "升序排序",
				},
				cli.BoolFlag{
					Name:  "desc",
					Usage: "降序排序",
				},
				cli.BoolFlag{
					Name:  "time",
					Usage: "根据时间排序",
				},
				cli.BoolFlag{
					Name:  "name",
					Usage: "根据文件名排序",
				},
				cli.BoolFlag{
					Name:  "size",
					Usage: "根据大小排序",
				},
			},
		},
		{
			Name:      "search",
			Aliases:   []string{"s"},
			Usage:     "搜索文件",
			UsageText: app.Name + " search [-path=<需要检索的目录>] [-r] 关键字",
			Description: `
	按文件名搜索文件（不支持查找目录）。
	默认在当前工作目录搜索.

	示例:

	搜索根目录的文件
	BaiduPCS-Go search -path=/ 关键字

	搜索当前工作目录的文件
	BaiduPCS-Go search 关键字

	递归搜索当前工作目录的文件
	BaiduPCS-Go search -r 关键字
`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				pcscommand.RunSearch(c.String("path"), c.Args().Get(0), &pcscommand.SearchOptions{
					Total:   c.Bool("l"),
					Recurse: c.Bool("r"),
				})

				return nil
			},
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "l",
					Usage: "详细显示",
				},
				cli.BoolFlag{
					Name:  "r",
					Usage: "递归搜索",
				},
				cli.StringFlag{
					Name:  "path",
					Usage: "需要检索的目录",
					Value: ".",
				},
			},
		},
		{
			Name:      "tree",
			Aliases:   []string{"t"},
			Usage:     "列出目录的树形图",
			UsageText: app.Name + " tree <目录>",
			Category:  "百度网盘",
			Before:    reloadFn,
			Action: func(c *cli.Context) error {
				pcscommand.RunTree(c.Args().Get(0))
				return nil
			},
		},
		{
			Name:      "pwd",
			Usage:     "输出工作目录",
			UsageText: app.Name + " pwd",
			Category:  "百度网盘",
			Before:    reloadFn,
			Action: func(c *cli.Context) error {
				fmt.Println(pcsconfig.Config.ActiveUser().Workdir)
				return nil
			},
		},
		{
			Name:        "meta",
			Usage:       "获取单个文件/目录的元信息",
			UsageText:   app.Name + " meta <文件/目录>",
			Description: "默认获取工作目录元信息",
			Category:    "百度网盘",
			Before:      reloadFn,
			Action: func(c *cli.Context) error {
				pcscommand.RunGetMeta(c.Args().Get(0))
				return nil
			},
		},
		{
			Name:      "rm",
			Usage:     "删除文件/目录",
			UsageText: app.Name + " rm <文件/目录的路径1> <文件/目录2> <文件/目录3> ...",
			Description: `
	注意: 删除多个文件和目录时, 请确保每一个文件和目录都存在, 否则删除操作会失败.
	被删除的文件或目录可在网盘文件回收站找回.

	示例:

	删除 /我的资源/1.mp4
	BaiduPCS-Go rm /我的资源/1.mp4

	删除 /我的资源/1.mp4 和 /我的资源/2.mp4
	BaiduPCS-Go rm /我的资源/1.mp4 /我的资源/2.mp4

	删除 /我的资源 内的所有文件和目录, 但不删除该目录
	BaiduPCS-Go rm /我的资源/*

	删除 /我的资源 整个目录 !!
	BaiduPCS-Go rm /我的资源
`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() == 0 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				pcscommand.RunRemove(c.Args()...)
				return nil
			},
		},
		{
			Name:      "mkdir",
			Usage:     "创建目录",
			UsageText: app.Name + " mkdir <目录>",
			Category:  "百度网盘",
			Before:    reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() == 0 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				pcscommand.RunMkdir(c.Args().Get(0))
				return nil
			},
		},
		{
			Name:  "cp",
			Usage: "拷贝文件/目录",
			UsageText: `BaiduPCS-Go cp <文件/目录> <目标文件/目录>
	BaiduPCS-Go cp <文件/目录1> <文件/目录2> <文件/目录3> ... <目标目录>`,
			Description: `
	注意: 拷贝多个文件和目录时, 请确保每一个文件和目录都存在, 否则拷贝操作会失败.

	示例:

	将 /我的资源/1.mp4 复制到 根目录 /
	BaiduPCS-Go cp /我的资源/1.mp4 /

	将 /我的资源/1.mp4 和 /我的资源/2.mp4 复制到 根目录 /
	BaiduPCS-Go cp /我的资源/1.mp4 /我的资源/2.mp4 /
`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() <= 1 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				pcscommand.RunCopy(c.Args()...)
				return nil
			},
		},
		{
			Name:  "mv",
			Usage: "移动/重命名文件/目录",
			UsageText: `移动:
	BaiduPCS-Go mv <文件/目录1> <文件/目录2> <文件/目录3> ... <目标目录>

	重命名:
	BaiduPCS-Go mv <文件/目录> <重命名的文件/目录>`,
			Description: `
	注意: 移动多个文件和目录时, 请确保每一个文件和目录都存在, 否则移动操作会失败.

	示例:

	将 /我的资源/1.mp4 移动到 根目录 /
	BaiduPCS-Go mv /我的资源/1.mp4 /

	将 /我的资源/1.mp4 重命名为 /我的资源/3.mp4
	BaiduPCS-Go mv /我的资源/1.mp4 /我的资源/3.mp4
`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() <= 1 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				pcscommand.RunMove(c.Args()...)
				return nil
			},
		},
		{
			Name:      "download",
			Aliases:   []string{"d"},
			Usage:     "下载文件/目录",
			UsageText: app.Name + " download <文件/目录路径1> <文件/目录2> <文件/目录3> ...",
			Description: `
	下载的文件默认保存到, 程序所在目录的 download/ 目录.
	通过 BaiduPCS-Go config set -savedir <savedir>, 自定义保存的目录.
	已支持目录下载.
	已支持多个文件或目录下载.
	已支持下载完成后自动校验文件, 但并不是所有的文件都支持校验!
	自动跳过下载重名的文件!

	示例:

	设置保存目录, 保存到 D:\Downloads
	注意区别反斜杠 "\" 和 斜杠 "/" !!!
	BaiduPCS-Go config set -savedir D:\\Downloads
	或者
	BaiduPCS-Go config set -savedir D:/Downloads

	下载 /我的资源/1.mp4
	BaiduPCS-Go d /我的资源/1.mp4

	下载 /我的资源 整个目录!!
	BaiduPCS-Go d /我的资源

	下载网盘内的全部文件!!
	BaiduPCS-Go d /
	BaiduPCS-Go d *
`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() == 0 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				var (
					saveTo string
				)

				if c.Bool("save") {
					saveTo = "."
				} else if c.String("saveto") != "" {
					saveTo = filepath.Clean(c.String("saveto"))
				}

				do := &pcscommand.DownloadOptions{
					IsTest:               c.Bool("test"),
					IsPrintStatus:        c.Bool("status"),
					IsExecutedPermission: c.Bool("x") && runtime.GOOS != "windows",
					IsOverwrite:          c.Bool("ow"),
					IsShareDownload:      c.Bool("share"),
					IsLocateDownload:     c.Bool("locate"),
					IsStreaming:          c.Bool("stream"),
					SaveTo:               saveTo,
					Parallel:             c.Int("p"),
					Load:                 c.Int("l"),
					MaxRetry:             c.Int("retry"),
					NoCheck:              c.Bool("nocheck"),
				}

				if c.Bool("bg") && isCli {
					pcscommand.RunBgDownload(c.Args(), do)
				} else {
					pcscommand.RunDownload(c.Args(), do)
				}

				return nil
			},
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "test",
					Usage: "测试下载, 此操作不会保存文件到本地",
				},
				cli.BoolFlag{
					Name:  "ow",
					Usage: "overwrite, 覆盖已存在的文件",
				},
				cli.BoolFlag{
					Name:  "status",
					Usage: "输出所有线程的工作状态",
				},
				cli.BoolFlag{
					Name:  "save",
					Usage: "将下载的文件直接保存到当前工作目录",
				},
				cli.StringFlag{
					Name:  "saveto",
					Usage: "将下载的文件直接保存到指定的目录",
				},
				cli.BoolFlag{
					Name:  "x",
					Usage: "为文件加上执行权限, (windows系统无效)",
				},
				cli.BoolFlag{
					Name:  "stream",
					Usage: "以流式文件的方式下载",
				},
				cli.BoolFlag{
					Name:  "share",
					Usage: "以分享文件的方式获取下载链接来下载",
				},
				cli.BoolFlag{
					Name:  "locate",
					Usage: "以获取直链的方式来下载",
				},
				cli.IntFlag{
					Name:  "p",
					Usage: "指定下载线程数",
				},
				cli.IntFlag{
					Name:  "l",
					Usage: "指定同时进行下载文件的数量",
				},
				cli.IntFlag{
					Name:  "retry",
					Usage: "下载失败最大重试次数",
					Value: pcscommand.DefaultDownloadMaxRetry,
				},
				cli.BoolFlag{
					Name:  "nocheck",
					Usage: "下载文件完成后不校验文件",
				},
				cli.BoolFlag{
					Name:  "bg",
					Usage: "加入后台下载",
				},
			},
		},
		{
			Name:  "bg",
			Usage: "管理后台任务",
			Description: `
	默认关闭下载中任何向终端的输出
	再后台进行文件下载，不会影响用户继续在客户端操作
	可以同时进行多个任务

	示例:

	显示所有后台任务
	BaiduPCS-Go bg
`,
			Category: "其他",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() == 0 {
					pcscommand.BgMap.PrintAllBgTask()
					return nil
				}
				return nil
			},
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "test",
					Usage: "测试下载, 此操作不会保存文件到本地",
				},
			},
		},
		{
			Name:      "upload",
			Aliases:   []string{"u"},
			Usage:     "上传文件/目录",
			UsageText: app.Name + " upload <本地文件/目录的路径1> <文件/目录2> <文件/目录3> ... <目标目录>",
			Description: `
	上传默认采用分片上传的方式, 上传的文件将会保存到, <目标目录>.
	遇到同名文件将会自动覆盖!!
	当上传的文件名和网盘的目录名称相同时, 不会覆盖目录, 防止丢失数据.

	注意: 

	分片上传之后, 服务器可能会记录到错误的文件md5, 程序会在上传完成后的修复md5, 修复md5不一定能成功, 但文件的完整性是没问题的.
	禁用分片上传可以保证服务器记录到正确的md5.
	禁用分片上传时只能使用单线程上传, 指定的单个文件上传最大线程数将会无效.

	示例:

	1. 将本地的 C:\Users\Administrator\Desktop\1.mp4 上传到网盘 /视频 目录
	注意区别反斜杠 "\" 和 斜杠 "/" !!!
	BaiduPCS-Go upload C:/Users/Administrator/Desktop/1.mp4 /视频

	2. 将本地的 C:\Users\Administrator\Desktop\1.mp4 和 C:\Users\Administrator\Desktop\2.mp4 上传到网盘 /视频 目录
	BaiduPCS-Go upload C:/Users/Administrator/Desktop/1.mp4 C:/Users/Administrator/Desktop/2.mp4 /视频

	3. 将本地的 C:\Users\Administrator\Desktop 整个目录上传到网盘 /视频 目录
	BaiduPCS-Go upload C:/Users/Administrator/Desktop /视频

	4. 使用相对路径
	BaiduPCS-Go upload 1.mp4 /视频
`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() <= 1 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				subArgs := c.Args()

				pcscommand.RunUpload(subArgs[:c.NArg()-1], subArgs[c.NArg()-1], &pcscommand.UploadOptions{
					Parallel:       c.Int("p"),
					MaxRetry:       c.Int("retry"),
					NotRapidUpload: c.Bool("norapid"),
					NotFixMD5:      c.Bool("nofix"),
					NotSplitFile:   c.Bool("nosplit"),
				})
				return nil
			},
			Flags: []cli.Flag{
				cli.IntFlag{
					Name:  "p",
					Usage: "指定单个文件上传的最大线程数",
				},
				cli.IntFlag{
					Name:  "retry",
					Usage: "上传失败最大重试次数",
					Value: pcscommand.DefaultUploadMaxRetry,
				},
				cli.BoolFlag{
					Name:  "norapid",
					Usage: "不检测秒传",
				},
				cli.BoolFlag{
					Name:  "nofix",
					Usage: "在上传完成后不修复md5",
				},
				cli.BoolFlag{
					Name:  "nosplit",
					Usage: "禁用分片上传",
				},
			},
		},
		{
			Name:      "locate",
			Aliases:   []string{"lt"},
			Usage:     "获取下载直链",
			UsageText: fmt.Sprintf("%s locate <文件1> <文件2> ...", app.Name),
			Description: `
	获取下载直链

	若该功能无法正常使用, 提示"user is not authorized, hitcode:101", 尝试更换 User-Agent 为 netdisk:
	BaiduPCS-Go config set -user_agent "netdisk"
`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				pcscommand.RunLocateDownload(c.Args()...)
				return nil
			},
		},
		{
			Name:      "rapidupload",
			Aliases:   []string{"ru"},
			Usage:     "手动秒传文件",
			UsageText: fmt.Sprintf("%s rapidupload -length=<文件的大小> -md5=<文件的md5值> -slicemd5=<文件前256KB切片的md5值(可选)> -crc32=<文件的crc32值(可选)> <保存的网盘路径, 需包含文件名>", app.Name),
			Description: `
	使用此功能秒传文件, 前提是知道文件的大小, md5, 前256KB切片的 md5 (可选), crc32 (可选), 且百度网盘中存在一模一样的文件.
	上传的文件将会保存到网盘的目标目录.
	遇到同名文件将会自动覆盖! 

	示例:

	1. 如果秒传成功, 则保存到网盘路径 /test
	BaiduPCS-Go rapidupload -length=56276137 -md5=fbe082d80e90f90f0fb1f94adbbcfa7f -slicemd5=38c6a75b0ec4499271d4ea38a667ab61 -crc32=314332359 /test

	2. 精简一下, 如果秒传成功, 则保存到网盘路径 /test
	BaiduPCS-Go rapidupload -length=56276137 -md5=fbe082d80e90f90f0fb1f94adbbcfa7f /test
`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() <= 0 || !c.IsSet("md5") || !c.IsSet("length") {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				pcscommand.RunRapidUpload(c.Args().Get(0), c.String("md5"), c.String("slicemd5"), c.String("crc32"), c.Int64("length"))
				return nil
			},
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "md5",
					Usage: "文件的 md5 值",
				},
				cli.StringFlag{
					Name:  "slicemd5",
					Usage: "文件前 256KB 切片的 md5 值 (可选)",
				},
				cli.StringFlag{
					Name:  "crc32",
					Usage: "文件的 crc32 值 (可选)",
				},
				cli.Int64Flag{
					Name:  "length",
					Usage: "文件的大小",
				},
			},
		},
		{
			Name:      "createsuperfile",
			Aliases:   []string{"csf"},
			Usage:     "手动分片上传—合并分片文件",
			UsageText: fmt.Sprintf("%s createsuperfile -path=<保存的网盘路径, 需包含文件名> block1 block2 ... ", app.Name),
			Description: `
	block1, block2 ... 为文件分片的md5值
	上传的文件将会保存到网盘的目标目录.
	遇到同名文件将会自动覆盖! 

	示例:

	BaiduPCS-Go createsuperfile -path=1.mp4 ec87a838931d4d5d2e94a04644788a55 ec87a838931d4d5d2e94a04644788a55
`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				pcscommand.RunCreateSuperFile(c.String("path"), c.Args()...)
				return nil
			},
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "path",
					Usage: "保存的网盘路径",
					Value: "superfile",
				},
			},
		},
		{
			Name:      "sumfile",
			Aliases:   []string{"sf"},
			Usage:     "获取本地文件的秒传信息",
			UsageText: app.Name + " sumfile <本地文件的路径1> <本地文件的路径2> ...",
			Description: `
	获取本地文件的大小, md5, 前256KB切片的md5, crc32, 可用于秒传文件.

	示例:

	获取 C:\Users\Administrator\Desktop\1.mp4 的秒传信息
	BaiduPCS-Go sumfile C:/Users/Administrator/Desktop/1.mp4
`,
			Category: "其他",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() <= 0 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}

				for k, filePath := range c.Args() {
					lp, err := checksum.GetFileSum(filePath, &checksum.SumConfig{
						IsMD5Sum:      true,
						IsCRC32Sum:    true,
						IsSliceMD5Sum: true,
					})
					if err != nil {
						fmt.Printf("[%d] %s\n", k+1, err)
						continue
					}

					fmt.Printf("[%d] - [%s]:\n", k+1, filePath)

					strLength, strMd5, strSliceMd5, strCrc32 := strconv.FormatInt(lp.Length, 10), hex.EncodeToString(lp.MD5), hex.EncodeToString(lp.SliceMD5), strconv.FormatUint(uint64(lp.CRC32), 10)
					fileName := filepath.Base(filePath)

					tb := pcstable.NewTable(os.Stdout)
					tb.SetColumnAlignment([]int{tablewriter.ALIGN_LEFT, tablewriter.ALIGN_LEFT})
					tb.AppendBulk([][]string{
						[]string{"文件大小", strLength},
						[]string{"md5", strMd5},
						[]string{"前256KB切片的md5", strSliceMd5},
						[]string{"crc32", strCrc32},
						[]string{"秒传命令 (完整)", app.Name + " rapidupload -length=" + strLength + " -md5=" + strMd5 + " -slicemd5=" + strSliceMd5 + " -crc32=" + strCrc32 + " " + fileName},
						[]string{"秒传命令 (精简)", app.Name + " ru -length=" + strLength + " -md5=" + strMd5 + " " + fileName},
					})
					tb.Render()
					fmt.Printf("\n")
				}

				return nil
			},
		},
		{
			Name:      "share",
			Usage:     "分享文件/目录",
			UsageText: app.Name + " share",
			Category:  "百度网盘",
			Before:    reloadFn,
			Action: func(c *cli.Context) error {
				if c.NArg() < 2 {
					cli.ShowCommandHelp(c, c.Command.Name)
					return nil
				}
				return nil
			},
			Subcommands: []cli.Command{
				{
					Name:        "set",
					Aliases:     []string{"s"},
					Usage:       "设置分享文件/目录",
					UsageText:   app.Name + " share set <文件/目录1> <文件/目录2> ...",
					Description: `目前只支持创建私密链接.`,
					Action: func(c *cli.Context) error {
						if c.NArg() < 1 {
							cli.ShowCommandHelp(c, c.Command.Name)
							return nil
						}
						pcscommand.RunShareSet(c.Args(), nil)
						return nil
					},
				},
				{
					Name:      "list",
					Aliases:   []string{"l"},
					Usage:     "列出已分享文件/目录",
					UsageText: app.Name + " share list",
					Action: func(c *cli.Context) error {
						pcscommand.RunShareList()
						return nil
					},
				},
				{
					Name:        "cancel",
					Aliases:     []string{"c"},
					Usage:       "取消分享文件/目录",
					UsageText:   app.Name + " share cancel <shareid_1> <shareid_2> ...",
					Description: `目前只支持通过分享id (shareid) 来取消分享.`,
					Action: func(c *cli.Context) error {
						if c.NArg() < 1 {
							cli.ShowCommandHelp(c, c.Command.Name)
							return nil
						}
						pcscommand.RunShareCancel(converter.SliceStringToInt64(c.Args()))
						return nil
					},
				},
			},
		},
		{
			Name:      "export",
			Aliases:   []string{"ep"},
			Usage:     "导出文件/目录",
			UsageText: app.Name + " export <文件/目录1> <文件/目录2> ...",
			Description: `
	导出网盘内的文件或目录, 原理为秒传文件, 此操作会生成导出文件或目录的命令.

	注意!!! :
	并不是所有的文件都能导出成功, 程序会列出无法导出的文件列表

	示例:

	导出当前工作目录:
	BaiduPCS-Go export

	导出所有文件和目录, 并设置新的根目录为 /root 
	BaiduPCS-Go export -root=/root /

	导出 /我的资源
	BaiduPCS-Go export /我的资源
`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				pcspaths := c.Args()
				if len(pcspaths) == 0 {
					pcspaths = []string{"."}
				}

				pcscommand.RunExport(pcspaths, c.String("root"))
				return nil
			},
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "root",
					Usage: "设置要导出文件或目录的根路径, 可以是相对路径",
				},
			},
		},
		{
			Name:    "offlinedl",
			Aliases: []string{"clouddl", "od"},
			Usage:   "离线下载",
			Description: `支持http/https/ftp/电驴/磁力链协议
	离线下载同时进行的任务数量有限, 超出限制的部分将无法添加.

	示例:

	1. 将百度和腾讯主页, 离线下载到根目录 /
	BaiduPCS-Go offlinedl add -path=/ http://baidu.com http://qq.com

	2. 添加磁力链接任务
	BaiduPCS-Go offlinedl add magnet:?xt=urn:btih:xxx

	3. 查询任务ID为 12345 的离线下载任务状态
	BaiduPCS-Go offlinedl query 12345

	4. 取消任务ID为 12345 的离线下载任务
	BaiduPCS-Go offlinedl cancel 12345`,
			Category: "百度网盘",
			Before:   reloadFn,
			Action: func(c *cli.Context) error {
				if c.NumFlags() <= 0 || c.NArg() <= 0 {
					cli.ShowCommandHelp(c, c.Command.Name)
				}
				return nil
			},
			Subcommands: []cli.Command{
				{
					Name:      "add",
					Aliases:   []string{"a"},
					Usage:     "添加离线下载任务",
					UsageText: app.Name + " offlinedl add -path=<离线下载文件保存的路径> 资源地址1 地址2 ...",
					Action: func(c *cli.Context) error {
						if c.NArg() < 1 {
							cli.ShowCommandHelp(c, c.Command.Name)
							return nil
						}

						pcscommand.RunCloudDlAddTask(c.Args(), c.String("path"))
						return nil
					},
					Flags: []cli.Flag{
						cli.StringFlag{
							Name:  "path",
							Usage: "离线下载文件保存的路径, 默认为工作目录",
						},
					},
				},
				{
					Name:      "query",
					Aliases:   []string{"q"},
					Usage:     "精确查询离线下载任务",
					UsageText: app.Name + " offlinedl query 任务ID1 任务ID2 ...",
					Action: func(c *cli.Context) error {
						if c.NArg() < 1 {
							cli.ShowCommandHelp(c, c.Command.Name)
							return nil
						}

						taskIDs := converter.SliceStringToInt64(c.Args())

						if len(taskIDs) == 0 {
							fmt.Printf("未找到合法的任务ID, task_id\n")
							return nil
						}

						pcscommand.RunCloudDlQueryTask(taskIDs)
						return nil
					},
				},
				{
					Name:      "list",
					Aliases:   []string{"ls", "l"},
					Usage:     "查询离线下载任务列表",
					UsageText: app.Name + " offlinedl list",
					Action: func(c *cli.Context) error {
						pcscommand.RunCloudDlListTask()
						return nil
					},
				},
				{
					Name:      "cancel",
					Aliases:   []string{"c"},
					Usage:     "取消离线下载任务",
					UsageText: app.Name + " offlinedl cancel 任务ID1 任务ID2 ...",
					Action: func(c *cli.Context) error {
						if c.NArg() < 1 {
							cli.ShowCommandHelp(c, c.Command.Name)
							return nil
						}

						taskIDs := converter.SliceStringToInt64(c.Args())

						if len(taskIDs) == 0 {
							fmt.Printf("未找到合法的任务ID, task_id\n")
							return nil
						}

						pcscommand.RunCloudDlCancelTask(taskIDs)
						return nil
					},
				},
				{
					Name:      "delete",
					Aliases:   []string{"del", "d"},
					Usage:     "删除离线下载任务",
					UsageText: app.Name + " offlinedl delete 任务ID1 任务ID2 ...",
					Action: func(c *cli.Context) error {
						isClear := c.Bool("all")
						if c.NArg() < 1 && !isClear {
							cli.ShowCommandHelp(c, c.Command.Name)
							return nil
						}

						// 清空离线下载任务记录
						if isClear {
							pcscommand.RunCloudDlClearTask()
							return nil
						}

						// 删除特定的离线下载任务记录
						taskIDs := converter.SliceStringToInt64(c.Args())
						if len(taskIDs) == 0 {
							fmt.Printf("未找到合法的任务ID, task_id\n")
							return nil
						}

						pcscommand.RunCloudDlDeleteTask(taskIDs)
						return nil
					},
					Flags: []cli.Flag{
						cli.BoolFlag{
							Name:  "all",
							Usage: "清空离线下载任务记录, 程序不会进行二次确认, 谨慎操作!!!",
						},
					},
				},
			},
		},
		{
			Name:        "config",
			Usage:       "显示和修改程序配置项",
			Description: "显示和修改程序配置项",
			Category:    "配置",
			Before:      reloadFn,
			After:       saveFunc,
			Action: func(c *cli.Context) error {
				fmt.Printf("----\n运行 %s config set 可进行设置配置\n\n当前配置:\n", app.Name)
				pcsconfig.Config.PrintTable()
				return nil
			},
			Subcommands: []cli.Command{
				{
					Name:      "set",
					Usage:     "修改程序配置项",
					UsageText: app.Name + " config set [arguments...]",
					Description: `
	例子:
		BaiduPCS-Go config set -appid=260149
		BaiduPCS-Go config set -enable_https=false
		BaiduPCS-Go config set -user_agent="netdisk;1.0"
		BaiduPCS-Go config set -cache_size 16384 -max_parallel 200 -savedir D:/download`,
					Action: func(c *cli.Context) error {
						if c.NumFlags() <= 0 || c.NArg() > 0 {
							cli.ShowCommandHelp(c, c.Command.Name)
							return nil
						}

						if c.IsSet("appid") {
							pcsconfig.Config.SetAppID(c.Int("appid"))
						}
						if c.IsSet("enable_https") {
							pcsconfig.Config.SetEnableHTTPS(c.Bool("enable_https"))
						}
						if c.IsSet("user_agent") {
							pcsconfig.Config.SetUserAgent(c.String("user_agent"))
						}
						if c.IsSet("cache_size") {
							pcsconfig.Config.SetCacheSize(c.Int("cache_size"))
						}
						if c.IsSet("max_parallel") {
							pcsconfig.Config.SetMaxParallel(c.Int("max_parallel"))
						}
						if c.IsSet("max_download_load") {
							pcsconfig.Config.SetMaxDownloadLoad(c.Int("max_download_load"))
						}
						if c.IsSet("savedir") {
							pcsconfig.Config.SetSaveDir(c.String("savedir"))
						}

						err := pcsconfig.Config.Save()
						if err != nil {
							fmt.Println(err)
							return err
						}

						pcsconfig.Config.PrintTable()
						fmt.Printf("\n保存配置成功!\n\n")

						return nil
					},
					Flags: []cli.Flag{
						cli.IntFlag{
							Name:  "appid",
							Usage: "百度 PCS 应用ID",
						},
						cli.BoolFlag{
							Name:  "enable_https",
							Usage: "启用 https",
						},
						cli.StringFlag{
							Name:  "user_agent",
							Usage: "浏览器标识",
						},
						cli.IntFlag{
							Name:  "max_parallel",
							Usage: "上传/下载网络连接的最大并发量",
						},
						cli.IntFlag{
							Name:  "cache_size",
							Usage: "下载缓存",
						},
						cli.IntFlag{
							Name:  "max_download_load",
							Usage: "同时进行下载文件的最大数量",
						},
						cli.StringFlag{
							Name:  "savedir",
							Usage: "下载文件的储存目录",
						},
					},
				},
			},
		},
		{
			Name:  "tool",
			Usage: "工具箱",
			Action: func(c *cli.Context) error {
				cli.ShowCommandHelp(c, c.Command.Name)
				return nil
			},
			Subcommands: []cli.Command{
				{
					Name:  "showtime",
					Usage: "显示当前时间(北京时间)",
					Action: func(c *cli.Context) error {
						fmt.Printf(pcstime.BeijingTimeOption("printLog"))
						return nil
					},
				},
				{
					Name:  "getip",
					Usage: "获取IP地址",
					Action: func(c *cli.Context) error {
						fmt.Printf("内部IP地址: \n")
						for _, address := range pcsutil.ListAddresses() {
							fmt.Printf("%s\n", address)
						}
						fmt.Printf("\n")

						ipAddr, err := getip.IPInfo(pcsconfig.Config.EnableHTTPS())
						if err != nil {
							fmt.Printf("获取外部IP错误: %s\n", err)
							return nil
						}

						fmt.Printf("外部IP地址: %s\n", ipAddr)
						return nil
					},
				},
				{
					Name:        "enc",
					Usage:       "加密文件",
					UsageText:   app.Name + " enc -method=<method> -key=<key> [files...]",
					Description: cryptoDescription,
					Action: func(c *cli.Context) error {
						if c.NArg() <= 0 {
							cli.ShowCommandHelp(c, c.Command.Name)
							return nil
						}

						for _, filePath := range c.Args() {
							encryptedFilePath, err := pcsutil.EncryptFile(c.String("method"), []byte(c.String("key")), filePath, !c.Bool("disable-gzip"))
							if err != nil {
								fmt.Printf("%s\n", err)
								continue
							}

							fmt.Printf("加密成功, %s -> %s\n", filePath, encryptedFilePath)
						}

						return nil
					},
					Flags: []cli.Flag{
						cli.StringFlag{
							Name:  "method",
							Usage: "加密方法",
							Value: "aes-128-ctr",
						},
						cli.StringFlag{
							Name:  "key",
							Usage: "加密密钥",
							Value: app.Name,
						},
						cli.BoolFlag{
							Name:  "disable-gzip",
							Usage: "不启用GZIP",
						},
					},
				},
				{
					Name:        "dec",
					Usage:       "解密文件",
					UsageText:   app.Name + " dec -method=<method> -key=<key> [files...]",
					Description: cryptoDescription,
					Action: func(c *cli.Context) error {
						if c.NArg() <= 0 {
							cli.ShowCommandHelp(c, c.Command.Name)
							return nil
						}

						for _, filePath := range c.Args() {
							decryptedFilePath, err := pcsutil.DecryptFile(c.String("method"), []byte(c.String("key")), filePath, !c.Bool("disable-gzip"))
							if err != nil {
								fmt.Printf("%s\n", err)
								continue
							}

							fmt.Printf("解密成功, %s -> %s\n", filePath, decryptedFilePath)
						}

						return nil
					},
					Flags: []cli.Flag{
						cli.StringFlag{
							Name:  "method",
							Usage: "加密方法",
							Value: "aes-128-ctr",
						},
						cli.StringFlag{
							Name:  "key",
							Usage: "加密密钥",
							Value: app.Name,
						},
						cli.BoolFlag{
							Name:  "disable-gzip",
							Usage: "不启用GZIP",
						},
					},
				},
			},
		},
		{
			Name:        "clear",
			Aliases:     []string{"cls"},
			Usage:       "清空控制台",
			UsageText:   app.Name + " clear",
			Description: "清空控制台屏幕",
			Category:    "其他",
			Action: func(c *cli.Context) error {
				pcsliner.ClearScreen()
				return nil
			},
		},
		{
			Name:    "quit",
			Aliases: []string{"exit"},
			Usage:   "退出程序",
			Action: func(c *cli.Context) error {
				return cli.NewExitError("", 0)
			},
			Hidden:   true,
			HideHelp: true,
		},
	}

	sort.Sort(cli.FlagsByName(app.Flags))
	sort.Sort(cli.CommandsByName(app.Commands))

	app.Run(os.Args)
}

// �
