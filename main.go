package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/knadh/smtppool"
	"github.com/schollz/progressbar/v3"
)

type Args struct {
	SMTPServer            string
	EHLODomain            string
	Username              string
	Password              string
	From                  string
	To                    string
	ConcurrentConnections int
	DurationSeconds       int
	Port                  int
	TimeoutSeconds        int
	EmailCount            int
	AttachmentPaths       []string
}

func main() {
	args := parseArgs()

	pool, err := createSMTPPool(args)
	if err != nil {
		log.Fatalf("Failed to create SMTP pool: %v", err)
	}
	defer pool.Close()

	email, err := buildEmail(args)
	if err != nil {
		log.Fatalf("Failed to build email: %v", err)
	}

	if args.EmailCount > 0 {
		fmt.Printf("Sending %d test emails...\n", args.EmailCount)
		sendMultipleEmails(pool, email, args)
	} else {
		runBenchmark(pool, email, args)
	}
}

func parseArgs() Args {
	args := Args{}
	flag.StringVar(&args.SMTPServer, "smtp-server", "", "SMTP server address")
	flag.StringVar(&args.EHLODomain, "ehlodomain", "localhost", "Custom EHLO domain")
	flag.StringVar(&args.Username, "username", "", "SMTP username")
	flag.StringVar(&args.Password, "password", "", "SMTP password")
	flag.StringVar(&args.From, "from", "", "Sender email address")
	flag.StringVar(&args.To, "to", "", "Recipient email address")
	flag.IntVar(&args.ConcurrentConnections, "concurrent-connections", 10, "Number of concurrent connections")
	flag.IntVar(&args.DurationSeconds, "duration-seconds", 60, "Duration of the benchmark in seconds")
	flag.IntVar(&args.Port, "port", 25, "SMTP server port")
	flag.IntVar(&args.TimeoutSeconds, "timeout-seconds", 10, "Connection timeout in seconds")
	flag.IntVar(&args.EmailCount, "email-count", 0, "Number of emails to send (overrides duration)")
	flag.Func("attachment", "Path to file to attach to the email", func(flagValue string) error {
		args.AttachmentPaths = append(args.AttachmentPaths, flagValue)
		return nil
	})
	flag.Parse()

	// Validate required arguments
	if args.SMTPServer == "" {
		log.Fatal("SMTP server address is required")
	}
	if args.From == "" {
		log.Fatal("Sender email address is required")
	}
	if args.To == "" {
		log.Fatal("Recipient email address is required")
	}
	if args.ConcurrentConnections < 1 {
		log.Fatal("Concurrent connections must be greater than 0")
	}

	return args
}

func createSMTPPool(args Args) (*smtppool.Pool, error) {
	config := smtppool.Opt{
		Host:        args.SMTPServer,
		HelloHostname: args.EHLODomain,
		Port:        args.Port,
		MaxConns:    args.ConcurrentConnections,
		IdleTimeout: time.Duration(args.TimeoutSeconds) * time.Second,
	}

	if args.Username != "" && args.Password != "" {
		// Use smtppool.LoginAuth to allow unencrypted auth (no TLS)
		auth := &smtppool.LoginAuth{
			Username: args.Username,
			Password: args.Password,
		}
		config.Auth = auth
	}
	return smtppool.New(config)
}

func addAttachments(email *smtppool.Email, filepaths []string) error {
	for _, filepath := range filepaths {
		if filepath == "" {
			continue
		}

		file, err := os.Open(filepath)
		if err != nil {
			return fmt.Errorf("failed to open attachment file: %v", err)
		}

		filename := path.Base(filepath)
		email.Attach(file, filename, "")

		file.Close()
	}

	return nil
}

func buildEmail(args Args) (smtppool.Email, error) {
	email := smtppool.Email{
		From:    args.From,
		To:      []string{args.To},
		Subject: "SMTP Benchmark",
		Text:    []byte("This is a test email for SMTP benchmarking."),
	}

	if err := addAttachments(&email, args.AttachmentPaths); err != nil {
		return email, err
	}

	return email, nil
}

func sendEmail(pool *smtppool.Pool, email smtppool.Email) error {
	return pool.Send(email)
}

func runBenchmark(pool *smtppool.Pool, email smtppool.Email, args Args) {
	startTime := time.Now()
	endTime := startTime.Add(time.Duration(args.DurationSeconds) * time.Second)

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, args.ConcurrentConnections)
	var totalSent int64
	var totalLatencyNanos int64

	bar := progressbar.NewOptions(-1,
		progressbar.OptionSetDescription("Sending emails"),
		progressbar.OptionSetWidth(50),
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetTheme(progressbar.Theme{Saucer: "=", SaucerHead: ">", SaucerPadding: " ", BarStart: "[", BarEnd: "]"}),
	)

	for time.Now().Before(endTime) {
		wg.Add(1)
		semaphore <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-semaphore }()

			start := time.Now()
			err := sendEmail(pool, email)
			latency := time.Since(start)

			if err != nil {
				log.Printf("Error sending email: %v", err)
			} else {
				atomic.AddInt64(&totalSent, 1)
				atomic.AddInt64(&totalLatencyNanos, int64(latency))
				bar.Add(1)
			}
		}()
	}

	wg.Wait()
	bar.Finish()
	totalDuration := time.Since(startTime)
	printResults(atomic.LoadInt64(&totalSent), time.Duration(atomic.LoadInt64(&totalLatencyNanos)), totalDuration)
}

func sendMultipleEmails(pool *smtppool.Pool, email smtppool.Email, args Args) {
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, args.ConcurrentConnections)
	var totalSent int64
	var totalLatencyNanos int64
	startTime := time.Now()

	bar := progressbar.NewOptions(args.EmailCount,
		progressbar.OptionSetDescription("Sending emails"),
		progressbar.OptionSetWidth(50),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetTheme(progressbar.Theme{Saucer: "=", SaucerHead: ">", SaucerPadding: " ", BarStart: "[", BarEnd: "]"}),
	)

	for i := 0; i < args.EmailCount; i++ {
		wg.Add(1)
		semaphore <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-semaphore }()

			start := time.Now()
			err := sendEmail(pool, email)
			latency := time.Since(start)

			if err != nil {
				log.Printf("Error sending email: %v", err)
			} else {
				atomic.AddInt64(&totalSent, 1)
				atomic.AddInt64(&totalLatencyNanos, int64(latency))
				bar.Add(1)
			}
		}()
	}

	wg.Wait()
	bar.Finish()
	totalDuration := time.Since(startTime)
	printResults(atomic.LoadInt64(&totalSent), time.Duration(atomic.LoadInt64(&totalLatencyNanos)), totalDuration)
}

func printResults(totalSent int64, totalLatency, totalDuration time.Duration) {
	if totalSent == 0 {
		fmt.Println("No emails were sent successfully")
		return
	}

	throughput := float64(totalSent) / totalDuration.Seconds()
	avgLatency := totalLatency / time.Duration(totalSent)

	fmt.Printf("Total emails sent: %d\n", totalSent)
	fmt.Printf("Throughput: %.2f emails/second\n", throughput)
	fmt.Printf("Average latency: %v\n", avgLatency)
	fmt.Printf("Total duration: %v\n", totalDuration)
}
