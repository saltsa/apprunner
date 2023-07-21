package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
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
		return
	}
	cr.running = true
	cr.Unlock()
	healthCheck := time.Tick(cmdHealthCheckInterval)
	for {
		select {
		case <-cr.reload:
			log.Printf("reload requested")

			cr.Stop()

			go func() {

				c := exec.Command(cr.Location)
				cr.Lock()
				cr.cmd = c
				cr.Unlock()

				c.WaitDelay = 5 * time.Second
				c.Env = append(os.Environ(), cr.Env...)
				errPipe, err := c.StderrPipe()
				if err != nil {
					log.Printf("pipefailure: %s", err)
					return
				}
				stdoutPipe, err := c.StdoutPipe()
				if err != nil {
					log.Printf("pipefailure: %s", err)
					return
				}
				printOutput(cr, errPipe)
				printOutput(cr, stdoutPipe)

				log.Printf("running application...")
				err = c.Start()
				if err == nil {
					log.Printf("app started")
				} else {
					log.Printf("app startup failure: %s", err)
					return
				}

				// release cmd resources
				err = c.Wait()
				if err != nil {
					log.Printf("wait failure: %s", err)
				}
				log.Printf("app wait success")
			}()

		case <-healthCheck:
			cr.RLock()
			log.Printf("health checking...")

			if cr.cmd != nil {
				ps := cr.cmd.ProcessState
				if ps != nil && !ps.Exited() {
					log.Printf("process still running")
				} else {
					log.Printf("process completed")
					select {
					case cr.reload <- struct{}{}:
					default:
					}
				}
			}
			cr.RUnlock()
		}
	}
}
