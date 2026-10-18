package main

import (
	"math"
	"math/rand"
	"testing"
)

func TestAssignMinimizesTotalCost(t *testing.T) {
	cost := [][]float64{
		{4, 1, 3},
		{2, 0, 5},
		{3, 2, 2},
	}
	got := mmAssign(cost)
	want := []int{1, 0, 2} // 1+2+2 = 5; greedy on row 0 would take column 1 and pay 3 more
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("assignment %v, want %v", got, want)
		}
	}
}

func TestAssignMoreColumnsThanRows(t *testing.T) {
	cost := [][]float64{
		{9, 9, 1, 9},
		{9, 2, 9, 9},
	}
	got := mmAssign(cost)
	if got[0] != 2 || got[1] != 1 {
		t.Fatalf("assignment %v, want [2 1]", got)
	}
	if mmAssign(nil) != nil {
		t.Fatal("no rows must give no assignment")
	}
}

func TestAssignMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 50; trial++ {
		n, m := 1+rng.Intn(5), 0
		m = n + rng.Intn(3)
		cost := make([][]float64, n)
		for i := range cost {
			cost[i] = make([]float64, m)
			for j := range cost[i] {
				cost[i][j] = rng.Float64() * 10
			}
		}
		got := mmAssign(cost)
		sum := 0.0
		seen := map[int]bool{}
		for i, j := range got {
			if seen[j] {
				t.Fatalf("column %d used twice in %v", j, got)
			}
			seen[j] = true
			sum += cost[i][j]
		}
		best := math.Inf(1)
		var rec func(i int, used uint, acc float64)
		rec = func(i int, used uint, acc float64) {
			if i == n {
				best = math.Min(best, acc)
				return
			}
			for j := 0; j < m; j++ {
				if used&(1<<j) == 0 {
					rec(i+1, used|1<<j, acc+cost[i][j])
				}
			}
		}
		rec(0, 0, 0)
		if math.Abs(sum-best) > 1e-9 {
			t.Fatalf("trial %d: cost %.4f, optimum %.4f", trial, sum, best)
		}
	}
}

func TestKalmanFollowsConstantVelocityAndCoasts(t *testing.T) {
	var k mmKF
	k.init(100, 100)
	dt := 0.2
	// a champion walking at 10 px/s along x
	for i := 1; i <= 25; i++ {
		k.predict(dt)
		k.update(100+10*dt*float64(i), 100, mmKFMeasStd)
	}
	if math.Abs(k.x.v-10) > 1.5 {
		t.Fatalf("velocity %.2f px/s, want about 10", k.x.v)
	}
	before := k.std()
	// lost for 3 s: it keeps going, the uncertainty grows
	for i := 0; i < 15; i++ {
		k.predict(dt)
	}
	if k.x.p < 100+10*dt*25 || k.x.p > 100+10*dt*25+30 {
		t.Fatalf("coasted to %.1f", k.x.p)
	}
	if k.std() <= before {
		t.Fatalf("uncertainty %.2f did not grow from %.2f while lost", k.std(), before)
	}
	// the velocity decays: after a long loss it stands still
	p0 := k.x.p
	for i := 0; i < 100; i++ {
		k.predict(dt)
	}
	if k.x.p-p0 > 25 {
		t.Fatalf("a lost champion kept walking: %.1f px in 20 s", k.x.p-p0)
	}
}

func TestKalmanCapsSpeed(t *testing.T) {
	var k mmKF
	k.init(10, 10)
	k.predict(0.2)
	k.update(60, 10, mmKFMeasStd) // a 50 px jump in one step (a blink or a wrong match)
	if math.Abs(k.x.v) > mmKFMaxSpeed+1e-9 {
		t.Fatalf("velocity %.1f above the cap", k.x.v)
	}
}
