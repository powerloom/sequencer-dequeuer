package service

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"sequencer-dequeuer/pkgs/reporting"
	"time"

	log "github.com/sirupsen/logrus"
)

var ReportingInstance *ReportingService

type ReportingService struct {
	url    string
	client *http.Client
}

func InitializeReportingService(url string, timeout time.Duration) {
	// Get the system certificate pool
	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		log.Warnf("Failed to get system certificate pool: %v. Creating new pool.", err)
		rootCAs = x509.NewCertPool()
	}

	// Create HTTP client with custom transport
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: rootCAs,
			},
		},
	}

	ReportingInstance = &ReportingService{
		url:    url,
		client: client,
	}
	go ReportingInstance.StartListening()
}

func (s *ReportingService) StartListening() {
	for issue := range reporting.SequencerAlertsChannel {
		s.SendFailureNotification(issue)
	}
}

// sendPostRequest sends a POST request to the specified URL
func (s *ReportingService) SendFailureNotification(issue reporting.SequencerAlert) {
	jsonData, err := json.Marshal(issue)
	if err != nil {
		log.Errorln("Unable to marshal notification: ", issue)
		return
	}
	req, err := http.NewRequest("POST", s.url, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Errorln("Error creating request: ", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send the request
	resp, err := s.client.Do(req)
	if err != nil {
		log.Errorf("Error sending request for issue %s: %s\n", issue.String(), err)
		// Handle error in case of failure
		return
	}
	defer resp.Body.Close()

	// Here you can handle response or further actions
	log.Debugln("Reporting service response status: ", resp.Status)
}
