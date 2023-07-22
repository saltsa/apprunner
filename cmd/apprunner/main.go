package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/saltsa/apprunner"
)

func main() {

	go func() {
		for {
			resp, err := apprunner.GetDeployConfig()
			if err != nil {
				log.Printf("failure to fetch deploy config: %s", err)
				time.Sleep(apprunner.ConfigReloadInterval)
				continue
			}

			// CleanRuns()
			for app, dc := range resp.Apps {
				cr := apprunner.NewCurrentRun(app, dc)
				if cr == nil {
					continue
				}
				cr.SetRunning(dc)
				go apprunner.RunApp(cr)
			}
			time.Sleep(apprunner.ConfigReloadInterval)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	for {
		recv := <-quit
		if recv == os.Interrupt {
			os.Stdout.Write([]byte("\r"))
		}

		log.Printf("got signal: %v", recv)

		if recv == os.Interrupt || recv == syscall.SIGTERM {
			log.Println("quitting")
			return
		}
	}
}
