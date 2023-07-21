package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func printOutput(cr *currentRun, rc io.ReadCloser) {
	go func() {
		scanner := bufio.NewScanner(rc)
		for scanner.Scan() {
			row := scanner.Text()
			fmt.Fprintf(os.Stdout, "%-17s | %s\n", cr.appName, row)
		}

		rc.Close()
	}()
}

// clean this
func runApp(cr *currentRun) {

	cr.Lock()
	if cr.running {
		cr.Unlock()
		return
	}
	cr.running = true
	cr.Unlock()
	healthCheck := time.Tick(cmdHealthCheckInterval)
	for {
		select {
		case <-cr.reload:
			log.Printf("reload requested")

			// makes sure the current process is stopped
			cr.Stop()

			cr.Lock()

			ctx, cancel := context.WithCancel(context.Background())
			c := exec.CommandContext(ctx, cr.Location)

			cr.cmd = c
			cr.ctx = ctx
			cr.cancelFunc = cancel

			c.WaitDelay = 5 * time.Second
			c.Cancel = func() error {
				log.Println("sending sigterm to process...")
				c.Process.Signal(syscall.SIGTERM)
				return nil
			}

			c.Env = append(os.Environ(), cr.Env...)

			errPipe, err := c.StderrPipe()
			if err != nil {
				log.Fatalf("pipefailure: %s", err)
			}
			stdoutPipe, err := c.StdoutPipe()
			if err != nil {
				log.Fatalf("pipefailure: %s", err)
			}
			printOutput(cr, errPipe)
			printOutput(cr, stdoutPipe)

			log.Printf("running application...")

			// this is non-blocking
			err = c.Start()
			if err == nil {
				log.Printf("app started successfully, pid=%d", c.Process.Pid)
			} else {
				log.Printf("app startup failure: %s", err)
			}

			cr.Unlock()

		case <-healthCheck:
			log.Printf("healthchecking...")

			cr.RLock()
			if cr.cmd != nil && cr.cmd.ProcessState != nil {
					if ps.Exited() {
						select {
						case cr.reload <- struct{}{}:
						default:
						}
					}
				}
			}
			cr.RUnlock()
		}
	}
}
