//go:build windows && console

package service

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"sondns/internal/buildinfo"
)

func consoleMenu(root string, input io.Reader, out io.Writer) error {
	r := bufio.NewReader(input)
	read := func(prompt string) (string, error) {
		fmt.Fprint(out, prompt)
		s, err := r.ReadString('\n')
		return strings.TrimSpace(s), err
	}
	for {
		fmt.Fprintf(out, "\n SonDNS CMD  %s\n ---------------------------------------\n 1  DNS logs\n 2  Change DNS\n 3  Components update\n 4  Settings\n 0  Close (DNS keeps running)\n", buildinfo.Version)
		choice, err := read("Choose: ")
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var args []string
		switch choice {
		case "0":
			return nil
		case "1":
			args = []string{"logs", "--follow"}
		case "2":
			fmt.Fprintln(out, "1 SonCMS | 2 NextDNS | 3 Custom | 0 Back")
			sub, e := read("DNS: ")
			if e != nil {
				return nil
			}
			switch sub {
			case "1":
				args = []string{"dns", "use", "SonCMS"}
			case "2":
				args = []string{"dns", "use", "NextDNS"}
			case "3":
				value, e := read("Encrypted upstream URL/stamp: ")
				if e != nil {
					return nil
				}
				args = []string{"dns", "custom", value}
			default:
				continue
			}
		case "3":
			args = []string{"components", "update"}
		case "4":
			fmt.Fprintln(out, "1 Status | 2 DPI | 3 DNSSEC | 4 Start with Windows | 5 Restart DNS | 6 Roll back update | 0 Back")
			sub, e := read("Settings: ")
			if e != nil {
				return nil
			}
			switch sub {
			case "1":
				args = []string{"doctor"}
			case "2":
				fmt.Fprintln(out, "1 Off | 2 Default | 3 TCP | 4 TCP + UDP | 0 Back")
				value, e := read("DPI: ")
				if e != nil {
					return nil
				}
				mode, ok := map[string]string{"1": "off", "2": "default", "3": "tcp", "4": "tcp+udp"}[value]
				if !ok {
					continue
				}
				args = []string{"dpi", mode}
			case "3", "4":
				value, e := read("on / off (Enter to go back): ")
				if e != nil {
					return nil
				}
				if value == "" {
					continue
				}
				if sub == "3" {
					args = []string{"dns", "dnssec", value}
				} else {
					args = []string{"service", "auto", value}
				}
			case "5":
				args = []string{"service", "restart"}
			case "6":
				args = []string{"components", "rollback"}
			default:
				continue
			}
		default:
			fmt.Fprintln(out, "Choose a listed option.")
			continue
		}
		if err = ConsoleCLI(args); err != nil {
			fmt.Fprintln(out, "Error:", err)
		}
	}
}
