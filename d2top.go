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
	time      time.Time
	section   []Counter
	status    []Counter
	bytes     int64
	referer   []Counter
	useragent []Counter
	hits      int
}

const (
	alert_threshold  = 400             // average to go over to trigger alert
	alert_average_by = 60              // average over 60 buckets (60 * 2sec interval = 2min)
	flush_interval   = 2 * time.Second // interval to aggregate and flush, aka bucket size
	average_by       = 30              // number of buckets to average by (30 * 2sec interval = 1min)
)

var (
	main_view        *gocui.View
	status_view      *gocui.View
	sparks_view      *gocui.View
	time_view        *gocui.View
	averages_view    *gocui.View
	logdump_view     *gocui.View
	alerts_view      *gocui.View
	alert_state_chan chan bool
	maxX             int
	maxY             int
	t                TimeSeries
	status_output    chan string
	sparks_output    chan string
	averages_output  chan string
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

	rawlog_output := make(chan RawLogEvent)
	main_output := make(chan string)
	logdump_output := make(chan string)
	alerts_output := make(chan string)
	alert_state_chan = make(chan bool)
	status_output = make(chan string)
	sparks_output = make(chan string)
	averages_output = make(chan string)

	t = make([]*Bucket, 0)

	go updateTimeView(g)
	go updateMainView(g, main_output)
	go updateLogDumpView(g, logdump_output)
	go updateAlertsView(g, alerts_output, alert_state_chan)
	go updateStatusView(g, status_output)
	go updateSparksView(g, sparks_output)
	go updateAveragesView(g, averages_output)
	go readLog(rawlog_output, logdump_output)
	go bucketize(rawlog_output, main_output, alerts_output)

	err := g.MainLoop()
	if err != nil && err != gocui.Quit {
		log.Panicln(err)
	}

}

func bucketize(rawlog_output chan RawLogEvent, main_output, alerts_output chan string) {
	events := make([]*RawLogEvent, 0)
	flush := time.Tick(flush_interval)
	alert_fail_state := false

	for {
		select {
		case event := <-rawlog_output:
			events = append(events, &event)
		case <-flush:
			_ip := make(map[string]int)
			ip := make([]Counter, 0)
			timestamp := time.Now().Local()
			_section := make(map[string]int)
			section := make([]Counter, 0)
			_status := make(map[string]int)
			status := make([]Counter, 0)
			var bytes int64 = 0
			_referer := make(map[string]int)
			referer := make([]Counter, 0)
			_useragent := make(map[string]int)
			useragent := make([]Counter, 0)
			var hits int = 0

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

			events = events[0:0]

			// refactor these ..
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

			bucket := Bucket{ip, timestamp, section, status, bytes, referer, useragent, hits}
			t = append(t, &bucket)

			go func() {
				// this should really be refactored into methods of TimeSeries
				sparkline_width := int(math.Abs(math.Min(float64(len(t)-1), float64(maxX-38))))
				sparkline_start := len(t) - sparkline_width
				top_sections := int(math.Abs(math.Min(float64(len(t[len(t)-1].section)), float64(maxY-17))))

				averages_message := ""
				averages_message += fmt.Sprint(" avg hits: ", t.AverageHits(average_by))
				averages_message += fmt.Sprint("   avg bytes: ", t.AverageBytes(average_by))
				averages_output <- averages_message

				status_message := ""
				status_message += fmt.Sprintln(" total hits:  ", t.TotalHits())
				status_message += fmt.Sprintln(" total bytes: ", t.TotalBytes())
				status_output <- status_message

				sparks_message := " "
				hits_history := make([]float64, 0)
				bytes_history := make([]float64, 0)
				for _, b := range t[sparkline_start:] {
					hits_history = append(hits_history, float64(b.hits))
					bytes_history = append(bytes_history, float64(b.bytes))
				}
				sparks_message += spark.Line(hits_history)
				sparks_message += fmt.Sprint(" ", t.LastBucket().hits, "\n ")
				sparks_message += spark.Line(bytes_history)
				sparks_message += fmt.Sprint(" ", t.LastBucket().bytes, "\n ")
				sparks_output <- sparks_message

				message := ""
				for _, v := range t.LastBucket().section[:top_sections] {
					message += fmt.Sprint(" /", v.name, " : ", strconv.Itoa(v.count), "\n")
				}
				main_output <- message
			}()

			// alert on crossing threshold
			go func() {
				avg := t.AverageHits(alert_average_by)
				if avg > alert_threshold && !alert_fail_state {
					alert_fail_state = true
					alert_state_chan <- alert_fail_state
					message := []string{"avg hits- ", strconv.Itoa(avg), " in last 2m exceeded alert_threshold of ", strconv.Itoa(alert_threshold), " at ", time.Now().Local().String()}
					alerts_output <- strings.Join(message, "")
				}
				if avg < alert_threshold && alert_fail_state {
					alert_fail_state = false
					alert_state_chan <- alert_fail_state
					message := []string{"avg hits- ", strconv.Itoa(avg), " in last 2m below alert_threshold of ", strconv.Itoa(alert_threshold), " at ", time.Now().Local().String()}
					alerts_output <- strings.Join(message, "")
				}
			}()

		}
	}
}

func updateStatusView(g *gocui.Gui, status_output chan string) {
	status_view, _ := g.View("status")
	for i := range status_output {
		status_view.Clear()
		fmt.Fprintln(status_view, i)
		g.Flush()
	}
}

func updateSparksView(g *gocui.Gui, sparks_output chan string) {
	sparks_view, _ := g.View("sparks")
	for i := range sparks_output {
		sparks_view.Clear()
		fmt.Fprintln(sparks_view, i)
		g.Flush()
	}
}

func updateAveragesView(g *gocui.Gui, averages_output chan string) {
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

func updateMainView(g *gocui.Gui, main_output chan string) {
	main_view, _ := g.View("main")
	for i := range main_output {
		main_view.Clear()
		fmt.Fprintln(main_view, i)
		g.Flush()
	}
}

func updateLogDumpView(g *gocui.Gui, logdump_output chan string) {
	logdump_view, _ := g.View("logdump_view")
	flush := time.Tick(2 * time.Second)
	for {
		select {
		case log_data := <-logdump_output:
			fmt.Fprintln(logdump_view, log_data)
		case <-flush:
			g.Flush()
		}
	}
}

func updateAlertsView(g *gocui.Gui, alerts_output chan string, alert_state_chan chan bool) {
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

func readLog(c chan RawLogEvent, logdump_output chan string) {
	var seek = tail.SeekInfo{Offset: 0, Whence: 2}
	tailer, err := tail.TailFile(os.Args[1], tail.Config{
		Follow:   true,
		ReOpen:   true,
		Location: &seek,
	})
	if err != nil {
		log.Panicln(err)
	}
	re := regexp.MustCompile(`^(?P<ip>[\d\.]+) - - \[(?P<time>.*)\] "(?P<verb>.*) (?P<query>.*) (?P<proto>.*)" (?P<status>\d+) (?P<bytes>\d+) "(?P<referer>.*)" "(?P<useragent>.*)"`)
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

		c <- logline
		logdump_output <- res[0]
	}
}

func layout(g *gocui.Gui) error {
	maxX, maxY = g.Size()
	if main_view, err := g.SetView("main", -1, 6, maxX, maxY-16); err != nil &&
		err != gocui.ErrorUnkView {
		return err
	} else {
		main_view.Autoscroll = true
		main_view.Frame = false
	}

	if status_view, err := g.SetView("status", 0, 3, 26, 6); err != nil &&
		err != gocui.ErrorUnkView {
		return err
	} else {
		status_view.Frame = true
		status_view.FgColor = gocui.ColorGreen
	}

	if sparks_view, err := g.SetView("sparks", 26, 3, maxX-1, 6); err != nil &&
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

	if logdump_view, err := g.SetView("logdump_view", 0, maxY-16, maxX-1, maxY-8); err != nil &&
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