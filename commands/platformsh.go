/*
 * Copyright (c) 2021-present Fabien Potencier <fabien@symfony.com>
 *
 * This file is part of Symfony CLI project
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, either version 3 of the
 * License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program. If not, see <http://www.gnu.org/licenses/>.
 */

package commands

import (
	"bytes"
	_ "embed"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/kr/pty"
	"github.com/mitchellh/go-homedir"
	"github.com/rs/zerolog"
	"github.com/symfony-cli/console"
	"github.com/symfony-cli/symfony-cli/local/platformsh"
	"github.com/symfony-cli/symfony-cli/util"
)

type platformshCLI struct {
	Commands []*console.Command

	path string
}

func NewPlatformShCLI() (*platformshCLI, error) {
	p := &platformshCLI{}
	for _, command := range platformsh.Commands {
		command.Action = p.proxyPSHCmd(strings.TrimPrefix(command.Category+":"+command.Name, "cloud:"))
		command.Args = []*console.Arg{
			{Name: "anything", Slice: true, Optional: true},
		}
		command.Flags = append(command.Flags,
			&console.BoolFlag{Name: "no", Aliases: []string{"n"}},
			&console.BoolFlag{Name: "yes", Aliases: []string{"y"}},
		)
		if _, ok := platformshBeforeHooks[command.FullName()]; !ok {
			// do not parse flags if we don't have hooks
			command.FlagParsing = console.FlagParsingSkipped
		}
		p.Commands = append(p.Commands, command)
	}
	return p, nil
}

func (p *platformshCLI) getPath() string {
	if p.path != "" {
		return p.path
	}

	home, err := homedir.Dir()
	if err != nil {
		panic("unable to get home directory")
	}

	// the Platform.sh CLI is always available on the containers thanks to the configurator
	p.path = platformsh.BinaryPath(home)
	if !util.InCloud() {
		if cloudPath, err := platformsh.Install(home); err == nil {
			p.path = cloudPath
		}
	}
	return p.path
}

func (p *platformshCLI) PSHMainCommands() []*console.Command {
	names := map[string]bool{
		"cloud:project:list":       true,
		"cloud:environment:list":   true,
		"cloud:environment:branch": true,
		"cloud:tunnel:open":        true,
		"cloud:environment:ssh":    true,
		"cloud:environment:push":   true,
		"cloud:domain:list":        true,
		"cloud:variable:list":      true,
		"cloud:user:add":           true,
	}
	mainCmds := []*console.Command{}
	for _, command := range p.Commands {
		if names[command.FullName()] {
			mainCmds = append(mainCmds, command)
		}
	}
	return mainCmds
}

func (p *platformshCLI) proxyPSHCmd(commandName string) console.ActionFunc {
	return func(commandName string) console.ActionFunc {
		return func(ctx *console.Context) error {
			if hook, ok := platformshBeforeHooks["cloud:"+commandName]; ok && !console.IsHelp(ctx) {
				if err := hook(ctx); err != nil {
					return err
				}
			}

			cmd := p.executor(append([]string{ctx.Command.UserName}, ctx.Args().Slice()...))
			f, err := pty.Start(cmd)
			if err != nil {
				return err
			}
			_, err = io.Copy(cmd.Stdout, f)
			return err
		}
	}(commandName)
}

func (p *platformshCLI) executor(args []string) *exec.Cmd {
	env := []string{
		"PLATFORMSH_CLI_APPLICATION_NAME=Platform.sh CLI for Symfony",
		"PLATFORMSH_CLI_APPLICATION_EXECUTABLE=symfony",
		"XDEBUG_MODE=off",
		"PLATFORMSH_CLI_WRAPPED=1",
	}
	if util.InCloud() {
		env = append(env, "PLATFORMSH_CLI_UPDATES_CHECK=0")
	}
	args[0] = strings.TrimPrefix(args[0], "cloud:")
	cmd := exec.Command(p.getPath(), args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func (p *platformshCLI) RunInteractive(logger zerolog.Logger, projectDir string, args []string, debug bool, stdin io.Reader) (bytes.Buffer, bool) {
	var buf bytes.Buffer
	cmd := p.executor(args)
	if projectDir != "" {
		cmd.Dir = projectDir
	}
	if debug {
		cmd.Stdout = io.MultiWriter(&buf, os.Stdout)
		cmd.Stderr = io.MultiWriter(&buf, os.Stderr)
	} else {
		cmd.Stdout = &buf
		cmd.Stderr = &buf
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	logger.Debug().Str("cmd", strings.Join(cmd.Args, " ")).Msg("Executing Platform.sh CLI command interactively")
	if err := cmd.Run(); err != nil {
		return buf, false
	}
	return buf, true
}

func (p *platformshCLI) WrapHelpPrinter() func(w io.Writer, templ string, data interface{}) {
	currentHelpPrinter := console.HelpPrinter
	return func(w io.Writer, templ string, data interface{}) {
		switch cmd := data.(type) {
		case *console.Command:
			if strings.HasPrefix(cmd.Category, "cloud") {
				cmd := p.executor([]string{cmd.UserName, "--help"})
				cmd.Run()
			} else {
				currentHelpPrinter(w, templ, data)
			}
		default:
			currentHelpPrinter(w, templ, data)
		}
	}
}
