package main

import (
	"bytes"
	"html/template"
	"log"
	"os/exec"
	"time"
)

func (a *goBlog) preStartHooks() {
	cfg := a.cfg.Hooks
	for _, cmd := range cfg.PreStart {
		func(cmd string) {
			executeHookCommand("pre-start", cfg.Shell, cmd)
		}(cmd)
	}
}

type postHookFunc func(*post)

func (a *goBlog) postPostHooks(p *post) {
	// Hooks after post published
	for _, cmdTmplString := range a.cfg.Hooks.PostPost {
		go func(p *post, cmdTmplString string) {
			a.cfg.Hooks.executeTemplateCommand("post-post", cmdTmplString, map[string]interface{}{
				"URL":  a.fullPostURL(p),
				"Post": p,
			})
		}(p, cmdTmplString)
	}
	for _, f := range a.pPostHooks {
		go f(p)
	}
}

func (a *goBlog) postUpdateHooks(p *post) {
	// Hooks after post updated
	for _, cmdTmplString := range a.cfg.Hooks.PostUpdate {
		go func(p *post, cmdTmplString string) {
			a.cfg.Hooks.executeTemplateCommand("post-update", cmdTmplString, map[string]interface{}{
				"URL":  a.fullPostURL(p),
				"Post": p,
			})
		}(p, cmdTmplString)
	}
	for _, f := range a.pUpdateHooks {
		go f(p)
	}
}

func (a *goBlog) postDeleteHooks(p *post) {
	for _, cmdTmplString := range a.cfg.Hooks.PostDelete {
		go func(p *post, cmdTmplString string) {
			a.cfg.Hooks.executeTemplateCommand("post-delete", cmdTmplString, map[string]interface{}{
				"URL":  a.fullPostURL(p),
				"Post": p,
			})
		}(p, cmdTmplString)
	}
	for _, f := range a.pDeleteHooks {
		go f(p)
	}
}

func (cfg *configHooks) executeTemplateCommand(hookType string, tmpl string, data map[string]interface{}) {
	cmdTmpl, err := template.New("cmd").Parse(tmpl)
	if err != nil {
		log.Println("Failed to parse cmd template:", err.Error())
		return
	}
	var cmdBuf bytes.Buffer
	if err = cmdTmpl.Execute(&cmdBuf, data); err != nil {
		log.Println("Failed to execute cmd template:", err.Error())
		return
	}
	cmd := cmdBuf.String()
	executeHookCommand(hookType, cfg.Shell, cmd)
}

var hourlyHooks = []func(){}

func (a *goBlog) startHourlyHooks() {
	cfg := a.cfg.Hooks
	// Add configured hourly hooks
	for _, cmd := range cfg.Hourly {
		c := cmd
		f := func() {
			executeHookCommand("hourly", cfg.Shell, c)
		}
		hourlyHooks = append(hourlyHooks, f)
	}
	// When there are hooks, start ticker
	if len(hourlyHooks) > 0 {
		// Wait for next full hour
		tr := time.AfterFunc(time.Until(time.Now().Truncate(time.Hour).Add(time.Hour)), func() {
			// Execute once
			for _, f := range hourlyHooks {
				go f()
			}
			// Start ticker and execute regularly
			ticker := time.NewTicker(1 * time.Hour)
			a.shutdown.Add(func() {
				ticker.Stop()
				log.Println("Stopped hourly hooks")
			})
			for range ticker.C {
				for _, f := range hourlyHooks {
					go f()
				}
			}
		})
		a.shutdown.Add(func() {
			if tr.Stop() {
				log.Println("Canceled hourly hooks")
			}
		})
	}
}

func executeHookCommand(hookType, shell, cmd string) {
	log.Printf("Executing %v hook: %v", hookType, cmd)
	out, err := exec.Command(shell, "-c", cmd).CombinedOutput()
	if err != nil {
		log.Println("Failed to execute command:", err.Error())
	}
	if len(out) > 0 {
		log.Printf("Output:\n%v", string(out))
	}
}
