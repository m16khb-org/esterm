package main

import "math"

// Building blocks of the multi-object tracker: a constant-velocity Kalman filter per axis
// and the assignment of champions to candidate icons (Hungarian method).

// mmKF1 is a one-axis constant-velocity Kalman filter: position p and velocity v in
// pixels and pixels per second of the mmFrameSize frame, with the covariance of the pair.
// The velocity decays (mmKFVelDecay) so a champion that stops, or is lost for a while,
// does not coast across the map.
type mmKF1 struct {
	p, v          float64
	ppp, ppv, pvv float64
}

const (
	mmKFAccel    = 30.0 // px/s² standard deviation of the acceleration noise (champions start, stop and turn at once)
	mmKFMeasStd  = 1.0  // px standard deviation of an icon position measurement
	mmKFVelDecay = 2.0  // s time constant of the velocity decay
	mmKFMaxSpeed = 18.0 // px/s: the fastest a champion walks (~900 units/s on a 14,980-unit map drawn at 288 px) before blinks and dashes
	mmKFInitVar  = 4.0  // px² position variance of a fresh track
)

// init starts the filter at position p at rest.
func (k *mmKF1) init(p, vstd float64) {
	*k = mmKF1{p: p, ppp: mmKFInitVar, pvv: vstd * vstd}
}

// predict advances the filter by dt seconds.
func (k *mmKF1) predict(dt float64) {
	if dt <= 0 {
		return
	}
	rho := math.Exp(-dt / mmKFVelDecay)
	// x' = F x with F = [[1, dt], [0, rho]]
	p := k.p + k.v*dt
	v := k.v * rho
	ppp := k.ppp + 2*dt*k.ppv + dt*dt*k.pvv
	ppv := rho * (k.ppv + dt*k.pvv)
	pvv := rho * rho * k.pvv
	q := mmKFAccel * mmKFAccel
	dt2, dt3, dt4 := dt*dt, dt*dt*dt, dt*dt*dt*dt
	ppp += q * dt4 / 4
	ppv += q * dt3 / 2
	pvv += q * dt2
	k.p, k.v, k.ppp, k.ppv, k.pvv = p, v, ppp, ppv, pvv
}

// update folds in a position measurement z with variance r2 and caps the velocity.
func (k *mmKF1) update(z, r2 float64) {
	s := k.ppp + r2
	kp, kv := k.ppp/s, k.ppv/s
	y := z - k.p
	k.p += kp * y
	k.v += kv * y
	k.v = math.Max(-mmKFMaxSpeed, math.Min(mmKFMaxSpeed, k.v))
	ppp, ppv, pvv := k.ppp, k.ppv, k.pvv
	k.ppp = (1 - kp) * ppp
	k.ppv = (1 - kp) * ppv
	k.pvv = pvv - kv*ppv
	if k.pvv < 0 {
		k.pvv = 0
	}
}

// mmKF is the filter of one champion's position.
type mmKF struct{ x, y mmKF1 }

func (k *mmKF) init(x, y float64) {
	k.x.init(x, 6)
	k.y.init(y, 6)
}

func (k *mmKF) predict(dt float64) { k.x.predict(dt); k.y.predict(dt) }

func (k *mmKF) update(x, y, std float64) {
	r2 := std * std
	k.x.update(x, r2)
	k.y.update(y, r2)
}

// std is the standard deviation of the position estimate in pixels (both axes averaged).
func (k *mmKF) std() float64 { return math.Sqrt((k.x.ppp + k.y.ppp) / 2) }

// mmAssign matches rows to columns minimizing the total cost; every row gets a column
// (rows <= cols is required) and the result holds the column of each row. cost[i][j] is
// finite. It is the Hungarian method with row/column potentials, O(rows²·cols).
func mmAssign(cost [][]float64) []int {
	n := len(cost)
	if n == 0 {
		return nil
	}
	m := len(cost[0])
	inf := math.Inf(1)
	u := make([]float64, n+1)
	v := make([]float64, m+1)
	p := make([]int, m+1) // p[j]: row matched to column j (1-based, 0: none)
	way := make([]int, m+1)
	for i := 1; i <= n; i++ {
		p[0] = i
		j0 := 0
		minv := make([]float64, m+1)
		used := make([]bool, m+1)
		for j := range minv {
			minv[j] = inf
		}
		for {
			used[j0] = true
			i0, delta, j1 := p[j0], inf, 0
			for j := 1; j <= m; j++ {
				if used[j] {
					continue
				}
				cur := cost[i0-1][j-1] - u[i0] - v[j]
				if cur < minv[j] {
					minv[j], way[j] = cur, j0
				}
				if minv[j] < delta {
					delta, j1 = minv[j], j
				}
			}
			for j := 0; j <= m; j++ {
				if used[j] {
					u[p[j]] += delta
					v[j] -= delta
				} else {
					minv[j] -= delta
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}
		for j0 != 0 {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
		}
	}
	out := make([]int, n)
	for j := 1; j <= m; j++ {
		if p[j] != 0 {
			out[p[j]-1] = j - 1
		}
	}
	return out
}
