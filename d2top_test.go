package main

import (
	"fmt"
	"regexp"
	"testing"
	"time"
)

type _TestMonitorHitsConfig struct {
	buckets_to_create       int
	average_hits_per_bucket int
	expected_alert_text     string
	expected_alert_state    bool
}

func GenerateBucket(counts ...int64) (bucket Bucket) {
	hits := 210
	if len(counts) > 0 {
		hits = int(counts[0])
	}
	bytes := int64(62285)
	if len(counts) > 1 {
		bytes = counts[1]
	}
	bucket = Bucket{
		[]Counter{{"2.3.4.5", 60}, {"3.4.5.6", 70}, {"4.5.6.7", 80}},
		time.Now(),
		[]Counter{{"stuff", 60}, {"things", 70}, {"bits", 80}},
		[]Counter{{"200", 130}, {"500", 10}, {"404", 70}},
		bytes,
		[]Counter{{"/foo", 200}, {"/bar", 10}},
		[]Counter{{"Mozasauraus FoxSocks 0.1", 100}, {"Goggle Krome 50.1", 110}},
		hits,
	}
	return
}

func _TestMonitorHits(t *testing.T, c _TestMonitorHitsConfig) {
	ts = make([]*Bucket, 0)
	alert_state_chan = make(chan bool)
	alerts_output = make(chan string)
	for i := 0; i < c.buckets_to_create; i++ {
		b := GenerateBucket((int64(c.average_hits_per_bucket)))
		ts = append(ts, &b)
	}

	go MonitorHits()

	alert_text := <-alerts_output
	t.Log("Received alerts output: ", alert_text)
	if match, _ := regexp.MatchString(c.expected_alert_text, alert_text); !match {
		t.Error("Actual alert text [", alert_text, "] did not match expected string [", c.expected_alert_text, "]")
	}
	alert_state := <-alert_state_chan
	t.Log("Recieved alerts state change: ", alert_state)
	if alert_state != c.expected_alert_state {
		t.Error("Alert state is [", alert_state, "], should be [", c.expected_alert_state, "]")
	}
}

func TestMonitorExceedHitsThreshold(t *testing.T) {
	c := _TestMonitorHitsConfig{}
	c.buckets_to_create = 200
	c.average_hits_per_bucket = 401
	c.expected_alert_text = fmt.Sprint(
		"avg hits- ",
		c.average_hits_per_bucket,
		" in last 2m exceeded alert_threshold of ",
		alert_threshold,
		" at ")
	c.expected_alert_state = true
	alert_fail_state = false // set current state of alert
	_TestMonitorHits(t, c)
}

func TestMonitorBelowHitsThreshold(t *testing.T) {
	c := _TestMonitorHitsConfig{}
	c.buckets_to_create = 200
	c.average_hits_per_bucket = 399
	c.expected_alert_text = fmt.Sprint(
		"avg hits- ",
		c.average_hits_per_bucket,
		" in last 2m below alert_threshold of ",
		alert_threshold,
		" at ")
	c.expected_alert_state = false
	alert_fail_state = true
	_TestMonitorHits(t, c)
}

func TestCounter(t *testing.T) {
	//var ts TimeSeries

}
