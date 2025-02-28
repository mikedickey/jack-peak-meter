package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"unsafe"

	"github.com/xthexder/go-jack"
)

const (
	disableCursor = "\033[?25l"
	enableCursor  = "\033[?25h"
	moveCursorUp  = "\033[F"
)

var (
	counter int
)

var fillBlocks = []string{" ", "▏", "▎", "▍", "▌", "▋", "▊", "▉", "█"}

type visualizer struct {
	channels    int     // Amount of input channels
	buffer      int     // Smoothing graph with last n printed samples, set 1 to disable
	amplifer    float64 // Compensate weak audio signal with this ultimate amplifier value
	printValues bool
	printChnIdx bool

	additionalBuffer int
	avg              float32
	avgMain          []float32
	lastValues       [][]float32

	client  *jack.Client
	PortsIn []*jack.Port
}

func (v *visualizer) Start() error {
	var status int
	var clientName string

	// trying to establish JACK client
	for i := 0; i < 1000; i++ {
		clientName = fmt.Sprintf("spectrum analyser %d", i)
		v.client, status = jack.ClientOpen(clientName, jack.NoStartServer)
		if status == 0 {
			break
		}
	}
	if status != 0 {
		return fmt.Errorf("failed to initialize client, errcode: %d", status)
	}
	defer v.client.Close()

	// registering JACK callback
	if code := v.client.SetProcessCallback(v.process); code != 0 {
		return fmt.Errorf("failed to set process callback: %d", code)
	}
	v.client.OnShutdown(v.shutdown)

	fmt.Print(disableCursor) // disablingCursorblink
	fmt.Print("\n")

	// Activating client
	if code := v.client.Activate(); code != 0 {
		return fmt.Errorf("failed to activate client: %d", code)
	}

	// registering audio channels inputs and connecting them automatically to system monitor output
	for i := 1; i <= v.channels; i++ {
		portName := fmt.Sprintf("input_%d", i)
		port := v.client.PortRegister(portName, jack.DEFAULT_AUDIO_TYPE, jack.PortIsInput, 0)
		v.PortsIn = append(v.PortsIn, port)

		srcPortName := fmt.Sprintf("system:monitor_%d", i)
		dstPortName := fmt.Sprintf("%s:input_%d", clientName, i)

		code := v.client.Connect(srcPortName, dstPortName)
		if code != 0 {
			// fmt.Printf("Failed connecting port \"%s\" to\"%s\"\n", srcPortName, dstPortName)
		}
	}

	interrupted := make(chan bool)

	// signal handler
	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		v.shutdown()
		interrupted <- true

	}()

	buffer := int(v.client.GetBufferSize())
	v.additionalBuffer = v.calculateAdditionalBuffer(buffer)

	<-interrupted
	return nil
}

func getHighestSpread(samples []jack.AudioSample) jack.AudioSample {
	var winner jack.AudioSample
	for _, s := range samples {
		if s < 0 {
			s = -s
		}

		if s > winner {
			winner = s
		}
	}
	return winner
}

// JACK callback
func (v *visualizer) process(nframes uint32) int {
	counter += 1
	for i, port := range v.PortsIn {
		samples := port.GetBuffer(nframes)

		highest := float32(getHighestSpread(samples))
		highest *= float32(v.amplifer)

		v.avgMain[i] += highest

		if counter >= v.additionalBuffer {
			value := v.avgMain[i] / float32(v.additionalBuffer)
			v.updateCache(value, i)

			termWidth, termHeight := getTermWidthHeight()

			if termHeight < v.channels {
				fmt.Printf(">> Not sufficient space for bars <<\r")
			} else {
				v.printBar(v.getAvg(i), termWidth, i)

				if i+1 != v.channels { // do not print newline for last bar
					fmt.Print("\n")
				}
				v.avgMain[i] = 0
			}
		}

	}
	if counter >= v.additionalBuffer {
		counter = 0
		for i := 1; i < v.channels; i++ {
			fmt.Print(moveCursorUp)
		}
	}

	return 0
}

// JACK callback
func (v *visualizer) shutdown() {
	fmt.Print(enableCursor + "\n")
	v.client.Close()
}

func newVisualizer(channels, buffer int, amplifier float64, printValues, printChnIdx bool) visualizer {
	var lastValues [][]float32
	var avgMin []float32

	// preparing fixed-size lastValues struct
	for channel := 0; channel < channels; channel++ {
		var tmp []float32
		for frame := 0; frame < buffer; frame++ {
			tmp = append(tmp, 0.0)
		}

		lastValues = append(lastValues, tmp)
		avgMin = append(avgMin, 0.0)
	}

	return visualizer{
		channels,
		buffer,
		amplifier,
		printValues,
		printChnIdx,
		1,
		0.0,
		avgMin,
		lastValues,
		nil,
		[]*jack.Port{},
	}
}

func (v *visualizer) updateCache(value float32, channel int) {
	l := v.buffer - 1
	for i := l; i > 0; i-- {
		v.lastValues[channel][i] = v.lastValues[channel][i-1]
	}
	v.lastValues[channel][0] = value
}

func (v *visualizer) getAvg(channel int) float32 {
	var avg float32
	for _, v := range v.lastValues[channel] {
		avg += v
	}
	avg = avg / float32(v.buffer)
	if avg > 1 {
		avg = 1
	}
	return avg
}

func (v *visualizer) calculateAdditionalBuffer(frameSize int) int {
	if frameSize > 512 {
		return 1
	}
	return 512 / frameSize
}

func (v *visualizer) printBar(value float32, width, chanNumber int) {
	var bar = ""
	if v.printValues {
		width -= 10
		bar = fmt.Sprintf(" %.3f |", value)
	} else {
		width -= 4
		bar = " |"
	}

	if v.printChnIdx {
		width -= 5
		bar = fmt.Sprintf(" %2d:%s", chanNumber, bar)
	}

	bar = "\r" + bar

	fullBlocks := int(float32(width) * value)
	for i := 0; i < fullBlocks; i++ {
		bar += fillBlocks[8] // full block fill
	}

	if fullBlocks < width {
		fillBlockIdx := int((float32(width)*value - float32(fullBlocks)) * 8)
		bar += fillBlocks[fillBlockIdx] // transition block fill
	}

	for i := 0; i <= width-fullBlocks-2; i++ {
		bar += fillBlocks[0] // empty block fill
	}

	fmt.Print(bar + "| ")
}

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func getTermWidthHeight() (x, y int) {
	ws := &winsize{}
	retCode, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(syscall.Stdin),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(ws)))

	if int(retCode) == -1 {
		panic(errno)
	}
	x = int(ws.Col)
	y = int(ws.Row)
	return
}

func main() {
	var (
		printValues   *bool
		printChnIdx   *bool
		flagChannels  *int
		flagBuffer    *int
		flagAmplifier *float64
	)

	printValues = flag.Bool("values", false, "Print value before each channel of visualizer")
	printChnIdx = flag.Bool("index", false, "Print channel index before each channel of visualizer")

	flagChannels = flag.Int("channels", 2, "Amount of input channels")
	flagBuffer = flag.Int("buffer", 10, "Smoothing graph with last n printed samples, set 1 to disable")
	flagAmplifier = flag.Float64("amplify", 3.5, "Compensate weak audio signal with this ultimate amplifier value")
	flag.Parse()

	visualizer := newVisualizer(*flagChannels, *flagBuffer, *flagAmplifier, *printValues, *printChnIdx)
	err := visualizer.Start()
	if err != nil {
		panic(err)
	}
	fmt.Println("Bye!")
}
