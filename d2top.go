package main

import (
	"fmt"
	"github.com/hpcloud/tail"
	"github.com/joliv/spark"
	"github.com/jroimartin/gocui"
	"log"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Counter struct {
	name  string
	count int
}

type ByCount []Counter

func (a ByCount) Len() int           { return len(a) }
func (a ByCount) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByCount) Less(i, j int) bool { return a[i].count > a[j].count }

type RawLogEvent struct {
	ip                 string
	time               time.Time
	verb, query, proto string
	status             int
	bytes              int64
	referer, useragent string
}

type Bucket struct {
	ip        []Counter
	timestamp time.Time
	section   []Counter
	status    []Counter
	bytes     int64
	referer   []Counter
	useragent []Counter
	hits      int
}

const (
	alert_threshold          = 400             // average to go over to trigger alert
	alert_average_by         = 60              // average over 60 buckets (60 * 2sec interval = 2min)
	flush_interval           = 2 * time.Second // interval to aggregate and flush, aka bucket size
	average_by               = 30              // number of buckets to average by (30 * 2sec interval = 1min)
	update_logdump_frequency = 500             // how often to update the logdump view in ms
)

var (
	main_view        *gocui.View
	status_view      *gocui.View
	sparks_view      *gocui.View
	time_view        *gocui.View
	averages_view    *gocui.View
	logdump_view     *gocui.View
	alerts_view      *gocui.View
	alert_fail_state bool = false
	alert_state_chan chan bool
	maxX             int
	maxY             int
	ts               TimeSeries
	main_output      chan string
	status_output    chan string
	sparks_output    chan string
	averages_output  chan string
	logdump_output   chan string
	alerts_output    chan string
	rawlog_output    chan RawLogEvent
)

type TimeSeries []*Bucket

func (ts TimeSeries) TotalHits() (total_hits int64) {
	total_hits = 0
	for _, v := range ts {
		total_hits += int64(v.hits)
	}
	return
}

func (ts TimeSeries) TotalBytes() (total_bytes int64) {
	total_bytes = 0
	for _, v := range ts {
		total_bytes += int64(v.bytes)
	}
	return
}

func (ts TimeSeries) AverageHits(buckets int) (average_hits int) {
	average_hits = 0
	b := int(math.Abs(math.Min(float64(len(ts)), float64(buckets))))
	for _, v := range ts[len(ts)-b:] {
		average_hits += v.hits
	}
	average_hits = average_hits / b
	return
}

func (ts TimeSeries) AverageBytes(buckets int) (average_bytes int64) {
	average_bytes = 0
	b := int64(math.Min(float64(len(ts)), float64(buckets)))
	for _, v := range ts[int64(len(ts))-b:] {
		average_bytes += v.bytes
	}
	average_bytes = average_bytes / b
	return
}

func (ts TimeSeries) LastBucket() (bucket *Bucket) {
	bucket = ts[len(ts)-1]
	return
}

func main() {

	if len(os.Args) < 2 {
		fmt.Printf("Usage : %s </path/to/apache/access_log> \n ", os.Args[0])
		os.Exit(1)
	}

	g := gocui.NewGui()
	g.ShowCursor = true

	if err := g.Init(); err != nil {
		log.Panicln(err)
	}
	defer g.Close()

	g.SetLayout(layout)

	if err := g.SetKeybinding("", gocui.KeyCtrlC, gocui.ModNone, quit); err != nil {
		log.Panicln(err)
	}

	rawlog_output = make(chan RawLogEvent)
	alerts_output = make(chan string)
	averages_output = make(chan string)
	logdump_output = make(chan string)
	main_output = make(chan string)
	sparks_output = make(chan string)
	status_output = make(chan string)
	alert_state_chan = make(chan bool)

	ts = make([]*Bucket, 0)

	go updateTimeView(g)
	go updateMainView(g)
	go updateLogDumpView(g)
	go updateAlertsView(g)
	go updateStatusView(g)
	go updateSparksView(g)
	go updateAveragesView(g)
	go readLog()
	go bucketize()

	err := g.MainLoop()
	if err != nil && err != gocui.Quit {
		log.Panicln(err)
	}

}

func bucketize() {
	events := make([]*RawLogEvent, 0)
	flush := time.Tick(flush_interval)

	for {
		select {

		// append incoming RawLogEvent to events[]
		case event := <-rawlog_output:
			events = append(events, &event)

		case <-flush:
			// take all the log lines since the last flush,
			// generate stats, puts results in a bucket,
			// append the bucket to a slice,
			// fire off stats reporting to the UI
			// fire off an alert check/ui update
			_ip := make(map[string]int)
			_referer := make(map[string]int)
			_section := make(map[string]int)
			_status := make(map[string]int)
			_useragent := make(map[string]int)
			ip := make([]Counter, 0)
			referer := make([]Counter, 0)
			section := make([]Counter, 0)
			status := make([]Counter, 0)
			useragent := make([]Counter, 0)
			timestamp := time.Now().Local()
			var bytes int64 = 0
			var hits int = 0

			// roll up, aggregate, average out
			for _, event := range events {
				_ip[event.ip]++
				path := strings.Split(event.query, "/")[1]
				_section[path]++
				_status[strconv.Itoa(event.status)]++
				bytes += event.bytes
				_referer[event.referer]++
				_useragent[event.useragent]++
				hits++
			}

			// empty the events slice
			events = events[0:0]

			// ugh this needs refactoring, and is totally stupid. a result of learning go
			// while writing this code.. I used maps to count uniques and then learned that
			// you can't sort them, but you can implement a sortable struct that's exactly
			// like a map. (Or maybe you can use the sortable primitives on a type that is
			// a map and I'm just a go nub).. this just copies the maps into Counters and
			// sorts them, ideally, we could get rid of the maps just use Counter directly
			for k, v := range _ip {
				counter := Counter{k, v}
				ip = append(ip, counter)
				sort.Sort(ByCount(ip))
			}
			for k, v := range _section {
				counter := Counter{k, v}
				section = append(section, counter)
				sort.Sort(ByCount(section))
			}
			for k, v := range _status {
				counter := Counter{k, v}
				status = append(status, counter)
				sort.Sort(ByCount(status))
			}
			for k, v := range _referer {
				counter := Counter{k, v}
				referer = append(referer, counter)
				sort.Sort(ByCount(referer))
			}
			for k, v := range _useragent {
				counter := Counter{k, v}
				useragent = append(useragent, counter)
				sort.Sort(ByCount(useragent))
			}

			// put it in a bucket
			bucket := Bucket{ip, timestamp, section, status, bytes, referer, useragent, hits}

			// put the bucket in the time series slice
			ts = append(ts, &bucket)

			// draw stats
			go func() {
				// this should be refactored into TimeSeries methods
				sparkline_width := int(math.Abs(math.Min(float64(len(ts)-1), float64(maxX-38))))
				sparkline_start := len(ts) - sparkline_width
				top_sections := int(math.Abs(math.Min(float64(len(ts[len(ts)-1].section)), float64(maxY-17))))

				averages_message := ""
				averages_message += fmt.Sprint(" avg hits: ", ts.AverageHits(average_by))
				averages_message += fmt.Sprint("   avg bytes: ", ts.AverageBytes(average_by))
				averages_output <- averages_message

				status_message := ""
				status_message += fmt.Sprintln(" total hits:  ", ts.TotalHits())
				status_message += fmt.Sprintln(" total bytes: ", ts.TotalBytes())
				status_output <- status_message

				sparks_message := " "
				hits_history := make([]float64, 0)
				bytes_history := make([]float64, 0)
				for _, b := range ts[sparkline_start:] {
					hits_history = append(hits_history, float64(b.hits))
					bytes_history = append(bytes_history, float64(b.bytes))
				}
				sparks_message += spark.Line(hits_history)
				sparks_message += fmt.Sprint(" ", ts.LastBucket().hits, "\n ")
				sparks_message += spark.Line(bytes_history)
				sparks_message += fmt.Sprint(" ", ts.LastBucket().bytes, "\n ")
				sparks_output <- sparks_message

				message := ""
				for _, v := range ts.LastBucket().section[:top_sections] {
					message += fmt.Sprint(" /", v.name, " : ", strconv.Itoa(v.count), "\n")
				}
				main_output <- message
			}()

			// alert on crossing threshold
			go MonitorHits()
		}
	}
}

func MonitorHits() {
	avg := ts.AverageHits(alert_average_by)
	if avg > alert_threshold && !alert_fail_state {
		alert_fail_state = true
		message := []string{"avg hits- ", strconv.Itoa(avg), " in last 2m exceeded alert_threshold of ", strconv.Itoa(alert_threshold), " at ", time.Now().Local().String()}
		alerts_output <- strings.Join(message, "")
		alert_state_chan <- alert_fail_state
	}
	if avg < alert_threshold && alert_fail_state {
		alert_fail_state = false
		message := []string{"avg hits- ", strconv.Itoa(avg), " in last 2m below alert_threshold of ", strconv.Itoa(alert_threshold), " at ", time.Now().Local().String()}
		alerts_output <- strings.Join(message, "")
		alert_state_chan <- alert_fail_state
	}
}

// the UI could be a lot smoother if there was less Clear()
// and Flush() .. Flush() could run on an interval instead of
// inside these update*View functions
func updateStatusView(g *gocui.Gui) {
	status_view, _ := g.View("status")
	for i := range status_output {
		status_view.Clear()
		fmt.Fprintln(status_view, i)
		g.Flush()
	}
}

func updateSparksView(g *gocui.Gui) {
	sparks_view, _ := g.View("sparks")
	for i := range sparks_output {
		sparks_view.Clear()
		fmt.Fprintln(sparks_view, i)
		g.Flush()
	}
}

func updateAveragesView(g *gocui.Gui) {
	averages_view, _ := g.View("averages")
	for i := range averages_output {
		averages_view.Clear()
		fmt.Fprintln(averages_view, i)
		g.Flush()
	}
}

func updateTimeView(g *gocui.Gui) {
	for {
		time.Sleep(1 * time.Second)
		time_view, _ := g.View("time")
		time_view.Clear()
		fmt.Fprintln(time_view, "", time.Now().Local())
		if err := g.Flush(); err != nil {
			return
		}
	}
}

func updateMainView(g *gocui.Gui) {
	main_view, _ := g.View("main")
	for i := range main_output {
		main_view.Clear()
		fmt.Fprintln(main_view, i)
		g.Flush()
	}
}

func updateLogDumpView(g *gocui.Gui) {
	logdump_view, _ := g.View("logdump_view")
	flush := time.Tick(update_logdump_frequency * time.Millisecond)
	for {
		select {
		case log_data := <-logdump_output:
			fmt.Fprintln(logdump_view, log_data)
		case <-flush:
			g.Flush()
		}
	}
}

func updateAlertsView(g *gocui.Gui) {
	alerts_view, _ := g.View("alerts")
	for {
		select {
		case alert_text := <-alerts_output:
			fmt.Fprintln(alerts_view, alert_text)
			g.Flush()
		case alert_state := <-alert_state_chan:
			if alert_state {
				alerts_view.BgColor = gocui.ColorRed
			} else {
				alerts_view.BgColor = gocui.ColorDefault
			}
			g.Flush()
		}
	}
}

func readLog() {
	var seek = tail.SeekInfo{Offset: 0, Whence: 2}
	tailer, err := tail.TailFile(os.Args[1], tail.Config{
		Follow:   true,
		ReOpen:   true,
		Location: &seek,
	})
	if err != nil {
		log.Panicln(err)
	}

	re := regexp.MustCompile(`^(?P<ip>[\d\.]+) - - \[(?P<timestamp>.*)\] "(?P<verb>.*) (?P<query>.*) (?P<proto>.*)" (?P<status>\d+) (?P<bytes>\d+) "(?P<referer>.*)" "(?P<useragent>.*)"`)

	for line := range tailer.Lines {
		res := re.FindStringSubmatch(line.Text)
		ip := res[1]
		curtime := time.Now().Local()
		verb := res[3]
		query := res[4]
		proto := res[5]
		status, _ := strconv.Atoi(res[6])
		bytes, _ := strconv.ParseInt(res[7], 10, 64)
		referer := res[8]
		useragent := res[9]

		logline := RawLogEvent{ip, curtime, verb, query, proto, status, bytes, referer, useragent}

		rawlog_output <- logline
		logdump_output <- res[0] // Spraynard Kruger
	}
}

func layout(g *gocui.Gui) error {
	maxX, maxY = g.Size()
	if main_view, err := g.SetView("main", 0, 5, maxX-1, maxY-15); err != nil &&
		err != gocui.ErrorUnkView {
		return err
	} else {
		main_view.Autoscroll = true
		main_view.Frame = false
	}

	if status_view, err := g.SetView("status", 0, 2, 26, 5); err != nil &&
		err != gocui.ErrorUnkView {
		return err
	} else {
		status_view.Frame = true
		status_view.FgColor = gocui.ColorGreen
	}

	if sparks_view, err := g.SetView("sparks", 26, 2, maxX-1, 5); err != nil &&
		err != gocui.ErrorUnkView {
		return err
	} else {
		sparks_view.Frame = true
		sparks_view.FgColor = gocui.ColorCyan
	}

	if averages_view, err := g.SetView("averages", 0, 0, maxX-43, 2); err != nil &&
		err != gocui.ErrorUnkView {
		return err
	} else {
		averages_view.Frame = true
		averages_view.FgColor = gocui.ColorBlue
	}

	if time_view, err := g.SetView("time", maxX-43, 0, maxX-1, 2); err != nil &&
		err != gocui.ErrorUnkView {
		return err
	} else {
		time_view.Frame = true
	}

	if logdump_view, err := g.SetView("logdump_view", 0, maxY-15, maxX-1, maxY-7); err != nil &&
		err != gocui.ErrorUnkView {
		return err
	} else {
		logdump_view.Frame = true
		logdump_view.Autoscroll = true
		logdump_view.FgColor = gocui.ColorYellow
	}

	if alerts_view, err := g.SetView("alerts", 0, maxY-7, maxX-1, maxY-1); err != nil &&
		err != gocui.ErrorUnkView {
		return err
	} else {
		alerts_view.Frame = true
		alerts_view.Editable = true
		alerts_view.Overwrite = true
		alerts_view.Autoscroll = true
		alerts_view.Highlight = false
		alerts_view.Wrap = true
		g.SetCurrentView("alerts")
	}

	return nil
}

func quit(g *gocui.Gui, v *gocui.View) error {
	return gocui.Quit
}
