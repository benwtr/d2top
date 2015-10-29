
# d2top

Follows an apache log, generates stats, displays them in a console.

![d2top](img/d2top.gif)

I wrote this as a solution to a programming challenge and also to learn Go. This is literally the first code I've written in Go and I was learning it as I went along, so there's a few silly and/or inefficient bits.

The design was inspired by statsd. Logs stream in and the individual fields get parsed out. Every n seconds, there is a flush event- the log data gets rolled up, averaged out; URLs, error codes, user-agents, etc get added up and sorted by occurrence. Then these stats are written to a bucket and appended to a slice of buckets, and we do that every interval.

The UI was built with https://github.com/jroimartin/gocui

I've only tested it with the log generator mentioned below.

## Build

1. Ensure you have Go installed and $GOPATH set
2. Fetch d2top `git clone git@github.com:benwtr/d2top.git d2top && cd d2top`
3. Install dependencies `go get`
4. Build `go build`

## Run

1. For generating junk logs, I’ve been using https://github.com/tamtam180/apache_log_gen/ `gem install --no-ri --no-rdoc apache-loggen`
2. Start writing out a log `apache-loggen --rate=46 > access.log`
3. In a new terminal window or tab, start the d2top program `./d2top access.log`
