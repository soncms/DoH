package app

import (
	"flag"
	"fmt"
	"os"

	"golang.org/x/sys/windows/svc"
	"sondns/internal/buildinfo"
	appconfig "sondns/internal/config"
	"sondns/internal/gui"
	"sondns/internal/service"
)

func Main() {
	isService, _ := svc.IsWindowsService()
	if isService {
		configPath, err := service.ServiceRunConfigPath(service.PortableRoot(), os.Args[1:])
		if err != nil {
			os.Exit(1)
		}
		if err := service.RunWindowsService(configPath); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := cli(os.Args[1:]); err != nil {
		gui.ReportStartupError(err)
		os.Exit(1)
	}
}

func cli(args []string) error {
	if service.ConsoleBuild {
		return service.ConsoleCLI(args)
	}
	if len(args) == 0 {
		return gui.Run("", false)
	}
	switch args[0] {
	case "version", "--version":
		fmt.Println("SonDNS", buildinfo.Version, "(portable GUI, embedded runtime)")
		return nil
	case "verify":
		root, err := service.EnsureEmbeddedZapret2(nil)
		if err != nil {
			return err
		}
		fmt.Println("Embedded runtime verified:", root)
		return nil
	case "gui":
		fs := flag.NewFlagSet("gui", flag.ContinueOnError)
		config := fs.String("config", "", "path to JSON config (defaults to the owned service configuration)")
		tray := fs.Bool("tray", false, "start minimized in system tray")
		waitFor := fs.Uint("wait-for", 0, "wait for the previous SonDNS window to exit")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := service.WaitForGUIHandoff(uint32(*waitFor)); err != nil {
			return err
		}
		return gui.Run(*config, *tray)
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		config := fs.String("config", service.DefaultConfigPath(), "path to JSON config")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return service.RunForeground(*config)
	case "init":
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		config := fs.String("config", service.DefaultConfigPath(), "path to JSON config")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		path, err := service.PortableConfigPath(service.PortableRoot(), *config)
		if err != nil {
			return err
		}
		return service.SavePortableConfig(path, appconfig.Default())
	case "install":
		return service.InstallService(args[1:])
	case "uninstall":
		return service.UninstallService()
	case "stop":
		return service.StopService()
	case "status":
		return service.ServiceStatus()
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Println(`SonDNS - portable encrypted DNS resolver for Windows

Commands:
	verify     extract/verify embedded runtime locally without starting drivers
	version    show build version
  init       create a default JSON config
  run        run in the foreground
  install    install and start (service_auto_start controls startup)
  stop       stop the Windows service without removing it
  uninstall  stop and remove the service
  status     show service status

Examples:
  SonDNS init
  SonDNS install
  SonDNS run --config data\sondns.json`)
}
