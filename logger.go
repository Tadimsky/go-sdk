package statsig

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

const (
	maxEvents           = 1000
	gateExposureEvent   = "statsig::gate_exposure"
	configExposureEvent = "statsig::config_exposure"
	layerExposureEvent  = "statsig::layer_exposure"
)

type exposureEvent struct {
	EventName          string              `json:"eventName"`
	User               User                `json:"user"`
	Value              string              `json:"value"`
	Metadata           map[string]string   `json:"metadata"`
	SecondaryExposures []map[string]string `json:"secondaryExposures"`
	Time               int64               `json:"time"`
}

type logEventInput struct {
	Events          []interface{}   `json:"events"`
	StatsigMetadata statsigMetadata `json:"statsigMetadata"`
}

type logEventResponse struct{}

type logger struct {
	events    []interface{}
	transport *transport
	tick      *time.Ticker
	mu        sync.Mutex
}

func newLogger(transport *transport) *logger {
	log := &logger{
		events:    make([]interface{}, 0),
		transport: transport,
		tick:      time.NewTicker(time.Minute),
	}

	go log.backgroundFlush()

	return log
}

func (l *logger) backgroundFlush() {
	for range l.tick.C {
		l.flush(false)
	}
}

func (l *logger) logCustom(evt Event) {
	evt.User.PrivateAttributes = nil
	if evt.Time == 0 {
		evt.Time = time.Now().Unix() * 1000
	}
	l.logInternal(evt)
}

func (l *logger) logExposureWithEvaluationDetails(
	evt *exposureEvent,
	evalDetails *evaluationDetails,
) {
	if evalDetails != nil {
		evt.Metadata["reason"] = string(evalDetails.reason)
		evt.Metadata["configSyncTime"] = fmt.Sprint(evalDetails.configSyncTime)
		evt.Metadata["initTime"] = fmt.Sprint(evalDetails.initTime)
		evt.Metadata["serverTime"] = fmt.Sprint(evalDetails.serverTime)
	}
	l.logExposure(*evt)
}

func (l *logger) logExposure(evt exposureEvent) {
	evt.User.PrivateAttributes = nil
	if evt.Time == 0 {
		evt.Time = time.Now().Unix() * 1000
	}
	l.logInternal(evt)
}

func (l *logger) logInternal(evt interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.events = append(l.events, evt)
	if len(l.events) >= maxEvents {
		l.flushInternal(false)
	}
}

func (l *logger) logGateExposure(
	user User,
	gateName string,
	value bool,
	ruleID string,
	exposures []map[string]string,
	evalDetails *evaluationDetails,
) {
	evt := &exposureEvent{
		User:      user,
		EventName: gateExposureEvent,
		Metadata: map[string]string{
			"gate":      gateName,
			"gateValue": strconv.FormatBool(value),
			"ruleID":    ruleID,
		},
		SecondaryExposures: exposures,
	}
	l.logExposureWithEvaluationDetails(evt, evalDetails)
}

func (l *logger) logConfigExposure(
	user User,
	configName string,
	ruleID string,
	exposures []map[string]string,
	evalDetails *evaluationDetails,
) {
	evt := &exposureEvent{
		User:      user,
		EventName: configExposureEvent,
		Metadata: map[string]string{
			"config": configName,
			"ruleID": ruleID,
		},
		SecondaryExposures: exposures,
	}
	l.logExposureWithEvaluationDetails(evt, evalDetails)
}

func (l *logger) logLayerExposure(
	user User,
	config configBase,
	parameterName string,
	evalResult evalResult,
	evalDetails *evaluationDetails,
) {
	allocatedExperiment := ""
	exposures := evalResult.UndelegatedSecondaryExposures
	isExplicit := evalResult.ExplicitParamters[parameterName]

	if isExplicit {
		allocatedExperiment = evalResult.ConfigDelegate
		exposures = evalResult.SecondaryExposures
	}

	evt := &exposureEvent{
		User:      user,
		EventName: layerExposureEvent,
		Metadata: map[string]string{
			"config":              config.Name,
			"ruleID":              config.RuleID,
			"allocatedExperiment": allocatedExperiment,
			"parameterName":       parameterName,
			"isExplicitParameter": strconv.FormatBool(isExplicit),
		},
		SecondaryExposures: exposures,
	}
	l.logExposureWithEvaluationDetails(evt, evalDetails)
}

func (l *logger) flush(closing bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.flushInternal(closing)
}

func (l *logger) flushInternal(closing bool) {
	if closing {
		l.tick.Stop()
	}
	if len(l.events) == 0 {
		return
	}

	if closing {
		l.sendEvents(l.events)
	} else {
		go l.sendEvents(l.events)
	}

	l.events = make([]interface{}, 0)
}

func (l *logger) sendEvents(events []interface{}) {
	input := &logEventInput{
		Events:          events,
		StatsigMetadata: l.transport.metadata,
	}
	var res logEventResponse
	_ = l.transport.retryablePostRequest("/log_event", input, &res, maxRetries)
}
