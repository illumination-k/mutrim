package concurrency

import (
	"context"
	"testing"
)

func TestCounter(t *testing.T) {
	var c Counter
	c.Inc()
	c.Inc()
	if c.n != 2 {
		t.Errorf("n = %d", c.n)
	}
}

func TestSum(t *testing.T) {
	if got := Sum([]int{1, 2, 3}); got != 6 {
		t.Errorf("Sum = %d", got)
	}
}

func TestProduce(t *testing.T) {
	var got []int
	for v := range Produce([]int{1, 2}) {
		got = append(got, v)
	}
	if len(got) != 2 {
		t.Errorf("Produce = %v", got)
	}
}

func TestFirst(t *testing.T) {
	ch := make(chan int, 1)
	ch <- 7
	if v, ok := First(context.Background(), ch); v != 7 || !ok {
		t.Errorf("First = %d, %v", v, ok)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := First(ctx, make(chan int)); ok {
		t.Error("First after cancel")
	}
}

func TestHandoff(t *testing.T) {
	if Handoff(3) != 3 {
		t.Error("Handoff")
	}
}

func TestInit(t *testing.T) {
	Init()
	if Init() != 1 {
		t.Error("Init ran twice")
	}
}

func TestFlag(t *testing.T) {
	var f int32
	Flag(&f)
	if f != 1 {
		t.Error("Flag")
	}
}

func TestBuffered(t *testing.T) {
	if c := cap(Buffered(2)); c != 2 {
		t.Errorf("cap = %d", c)
	}
}
