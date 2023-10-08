package pvr

import (
	"fmt"
	"github.com/imroc/req"
	"github.com/l3uddz/wantarr/config"
	"github.com/l3uddz/wantarr/logger"
	"github.com/l3uddz/wantarr/utils/web"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"strings"
	"time"
	"encoding/json"
	"io"
)

/* Structs */

type RadarrV4 struct {
	cfg        *config.Pvr
	log        *logrus.Entry
	apiUrl     string
	reqHeaders req.Header
	timeout    int
}

type RadarrV4Movie struct {
	Id         int
	AirDateUtc time.Time `json:"inCinemas"`
	DigitalUtc time.Time `json:"digitalRelease"`
	PhysicalUtc time.Time `json:"physicalRelease"`
	Status     string
	Monitored  bool
	HasFile    bool
}

type RadarrV4SystemStatus struct {
	Version string
}

type RadarrV4CommandStatus struct {
	Name    string
	Message string
	Started time.Time
	Ended   time.Time
	Status  string
}

type RadarrV4CommandResponse struct {
	Id int
}

type RadarrV4MovieSearch struct {
	Name   string `json:"name"`
	Movies []int  `json:"movieIds"`
}

type RadarrV4Wanted struct {
	Records       []RadarrV4Movie
}

/* Initializer */

func NewRadarrV4(name string, c *config.Pvr) *RadarrV4 {
	// set api url
	apiUrl := ""
	if strings.Contains(c.URL, "/api/v3") {
		apiUrl = c.URL
	} else {
		apiUrl = web.JoinURL(c.URL, "/api/v3")
	}

	// set headers
	reqHeaders := req.Header{
		"X-Api-Key": c.ApiKey,
	}

	return &RadarrV4{
		cfg:        c,
		log:        logger.GetLogger(name),
		apiUrl:     apiUrl,
		reqHeaders: reqHeaders,
		timeout:    pvrDefaultTimeout,
	}
}

/* Private */

func (p *RadarrV4) getSystemStatus() (*RadarrV4SystemStatus, error) {
	// send request
	resp, err := web.GetResponse(web.GET, web.JoinURL(p.apiUrl, "/system/status"), p.timeout, p.reqHeaders,
		&pvrDefaultRetry)
	if err != nil {
		return nil, errors.New("failed retrieving system status api response from radarr")
	}
	defer resp.Response().Body.Close()

	// validate response
	if resp.Response().StatusCode != 200 {
		return nil, fmt.Errorf("failed retrieving valid system status api response from radarr: %s",
			resp.Response().Status)
	}

	// decode response
	var s RadarrV4SystemStatus
	if err := resp.ToJSON(&s); err != nil {
		return nil, errors.WithMessage(err, "failed decoding system status api response from radarr")
	}

	return &s, nil
}

func (p *RadarrV4) getCommandStatus(id int) (*RadarrV4CommandStatus, error) {
	// send request
	resp, err := web.GetResponse(web.GET, web.JoinURL(p.apiUrl, fmt.Sprintf("/command/%d", id)), p.timeout,
		p.reqHeaders, &pvrDefaultRetry)
	if err != nil {
		return nil, errors.New("failed retrieving command status api response from radarr")
	}
	defer resp.Response().Body.Close()

	// validate response
	if resp.Response().StatusCode != 200 {
		return nil, fmt.Errorf("failed retrieving valid command status api response from radarr: %s",
			resp.Response().Status)
	}

	// decode response
	var s RadarrV4CommandStatus
	if err := resp.ToJSON(&s); err != nil {
		return nil, errors.WithMessage(err, "failed decoding command status api response from radarr")
	}

	return &s, nil
}

/* Interface Implements */

func (p *RadarrV4) Init() error {
	// retrieve system status
	status, err := p.getSystemStatus()
	if err != nil {
		return errors.Wrap(err, "failed initializing radarr pvr")
	}

	// determine version
	switch status.Version[0:1] {
	case "4":
		break
	default:
		return fmt.Errorf("unsupported version of radarr pvr: %s", status.Version)
	}
	return nil
}

func (p *RadarrV4) GetQueueSize() (int, error) {
	// send request
	resp, err := web.GetResponse(web.GET, web.JoinURL(p.apiUrl, "/queue"), p.timeout, p.reqHeaders,
		&pvrDefaultRetry)
	if err != nil {
		return 0, errors.WithMessage(err, "failed retrieving queue api response from radarr")
	}
	defer resp.Response().Body.Close()

	// validate response
	if resp.Response().StatusCode != 200 {
		return 0, fmt.Errorf("failed retrieving valid queue api response from radarr: %s",
			resp.Response().Status)
	}

	// decode response
	var q []interface{}
	if err := resp.ToJSON(&q); err != nil {
		return 0, errors.WithMessage(err, "failed decoding queue api response from radarr")
	}

	queueSize := len(q)
	p.log.WithField("queue_size", queueSize).Debug("Queue retrieved")
	return queueSize, nil
}

func (p *RadarrV4) GetWantedMissing() ([]MediaItem, error) {
	// logic vars
	totalRecords := 0
	var wantedMissing []MediaItem

	// retrieve all page results
	p.log.Info("Retrieving wanted missing media...")

	// send request
	resp, err := web.GetResponse(web.GET, web.JoinURL(p.apiUrl, "/movie"), p.timeout,
		p.reqHeaders, &pvrDefaultRetry)
	if err != nil {
		return nil, errors.WithMessage(err, "failed retrieving movies api response from radarr")
	}

	// validate response
	if resp.Response().StatusCode != 200 {
		_ = resp.Response().Body.Close()
		return nil, fmt.Errorf("failed retrieving valid movies api response from radarr: %s",
			resp.Response().Status)
	}

	body, err := io.ReadAll(resp.Response().Body)
	if err != nil {
		_ = resp.Response().Body.Close()
		return nil, errors.WithMessage(err, "failed decoding movies api response from radarr")
	}

	var records []RadarrV4Movie
	if err := json.Unmarshal(body, &records); err != nil {
	 	_ = resp.Response().Body.Close()
	 	return nil, errors.WithMessage(err, "failed decoding movies api response from radarr")
	}

	// process response
	for _, movie := range records {
		// is this movie released?
		if !movie.Monitored || movie.Status != "released" || movie.HasFile {
			continue
		}

		//lets find the highest date.
		airDate := movie.AirDateUtc

		// Compare and update if necessary
		if !movie.PhysicalUtc.IsZero() && movie.PhysicalUtc.After(airDate) {
			airDate = movie.PhysicalUtc
		}

		if !movie.DigitalUtc.IsZero() && movie.DigitalUtc.After(airDate) {
			airDate = movie.DigitalUtc
		}

		// dont search this item its more than 31 days past release date
		if !airDate.IsZero() {
			maxDateToTry := airDate.Add((24 * time.Hour) * 31)
			if time.Now().UTC().After(maxDateToTry) {
				continue
			}
		}

		// store this movie
		wantedMissing = append(wantedMissing, MediaItem{
			ItemId:     movie.Id,
			AirDateUtc: airDate,
			LastSearch: time.Time{},
		})
	}
	totalRecords += len(records)

	// close response
	_ = resp.Response().Body.Close()

	p.log.WithField("media_items", totalRecords).Info("Finished")

	return wantedMissing, nil
}

func (p *RadarrV4) GetWantedCutoff() ([]MediaItem, error) {
	// logic vars
	totalRecords := 0
	var wantedCutoff []MediaItem
	
	//not worrying about this for now
	return wantedCutoff, nil

	page := 1
	lastPageSize := pvrDefaultPageSize

	// set params
	params := req.QueryParam{
		"pageSize":  pvrDefaultPageSize,
		"monitored": "true",
	}

	// retrieve all page results
	p.log.Info("Retrieving wanted cutoff unmet media...")

	for {
		// break loop when all pages retrieved
		if lastPageSize == 0 {
			break
		}

		// set page
		params["page"] = page

		// send request
		resp, err := web.GetResponse(web.GET, web.JoinURL(p.apiUrl, "/wanted/cutoff"), p.timeout,
			p.reqHeaders, &pvrDefaultRetry, params)
		if err != nil {
			return nil, errors.WithMessage(err, "failed retrieving wanted cutotff unmet api response from radarr")
		}

		// validate response
		if resp.Response().StatusCode != 200 {
			_ = resp.Response().Body.Close()
			return nil, fmt.Errorf("failed retrieving valid wanted cutoff unmet api response from radarr: %s",
				resp.Response().Status)
		}

		// decode response
		var m RadarrV4Wanted
		if err := resp.ToJSON(&m); err != nil {
			_ = resp.Response().Body.Close()
			return nil, errors.WithMessage(err, "failed decoding wanted cutoff unmet api response from radarr")
		}

		// process response
		lastPageSize = len(m.Records)
		for _, movie := range m.Records {
			// store this movie
			airDate := movie.AirDateUtc
			wantedCutoff = append(wantedCutoff, MediaItem{
				ItemId:     movie.Id,
				AirDateUtc: airDate,
				LastSearch: time.Time{},
			})
		}
		totalRecords += lastPageSize

		p.log.WithField("page", page).Debug("Retrieved")
		page += 1

		// close response
		_ = resp.Response().Body.Close()
	}

	p.log.WithField("media_items", totalRecords).Info("Finished")

	return wantedCutoff, nil
}

func (p *RadarrV4) SearchMediaItems(mediaItemIds []int) (bool, error) {
	// set request data
	payload := RadarrV2MovieSearch{
		Name:   "moviesSearch",
		Movies: mediaItemIds,
	}

	// send request
	resp, err := web.GetResponse(web.POST, web.JoinURL(p.apiUrl, "/command"), p.timeout, p.reqHeaders,
		&pvrDefaultRetry, req.BodyJSON(&payload))
	if err != nil {
		return false, errors.WithMessage(err, "failed retrieving command api response from radarr")
	}
	defer resp.Response().Body.Close()

	// validate response
	if resp.Response().StatusCode != 201 {
		return false, fmt.Errorf("failed retrieving valid command api response from radarr: %s",
			resp.Response().Status)
	}

	// decode response
	var q RadarrV2CommandResponse
	if err := resp.ToJSON(&q); err != nil {
		return false, errors.WithMessage(err, "failed decoding command api response from radarr")
	}

	// monitor search status
	p.log.WithField("command_id", q.Id).Debug("Monitoring search status")

	for {
		// retrieve command status
		searchStatus, err := p.getCommandStatus(q.Id)
		if err != nil {
			return false, errors.Wrapf(err, "failed retrieving command status from radarr for: %d", q.Id)
		}

		p.log.WithFields(logrus.Fields{
			"command_id": q.Id,
			"status":     searchStatus.Status,
		}).Debug("Status retrieved")

		// is status complete?
		if searchStatus.Status == "completed" {
			break
		} else if searchStatus.Status == "failed" {
			return false, fmt.Errorf("search failed with message: %q", searchStatus.Message)
		} else if searchStatus.Status != "started" && searchStatus.Status != "queued" {
			return false, fmt.Errorf("search failed with unexpected status %q, message: %q", searchStatus.Status, searchStatus.Message)
		}

		time.Sleep(10 * time.Second)
	}

	return true, nil
}
