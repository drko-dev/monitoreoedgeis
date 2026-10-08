package inference

import "math"

// minLatencySamples is how many inferences a camera needs before its
// service time counts as a capacity measurement.
const minLatencySamples = 10

// measureDevice derives device capacity from measured service time: the
// worker is serial, so capacity = 1000 / average latency. Utilization is
// the measured busy fraction (sum of effective FPS x latency).
func measureDevice(measured map[string]Measured, reserve float64) DeviceStatus {
	dev := DeviceStatus{CapacitySource: "unmeasured", ReserveFraction: reserve}
	var latSum, weight, busy float64
	for _, mm := range measured {
		dev.MeasuredThroughput += mm.EffectiveFPS
		if mm.LatencyMS > 0 && mm.EffectiveFPS > 0 && mm.Samples >= minLatencySamples {
			latSum += mm.LatencyMS * mm.EffectiveFPS
			weight += mm.EffectiveFPS
			busy += mm.EffectiveFPS * mm.LatencyMS / 1000
		}
	}
	if weight > 0 {
		dev.CapacityFPS = 1000 / (latSum / weight)
		dev.CapacitySource = "measured_latency"
		dev.AvailableFPS = dev.CapacityFPS * (1 - reserve)
		dev.Utilization = math.Min(busy, 1)
	}
	return dev
}

// allocate grants every camera its min first, then shares what is left
// over its extra demand by priority weight (weighted water-filling). When
// even the mins do not fit, mins are scaled down by weight and those
// cameras are reported saturated -- never a promise the worker cannot keep.
// Without a capacity measurement yet, demands pass through unchanged.
func allocate(ds []demand, available float64, measured bool) (map[string]float64, map[string]string) {
	alloc := make(map[string]float64, len(ds))
	reasons := map[string]string{}
	if !measured {
		for _, d := range ds {
			alloc[d.key] = d.want
		}
		return alloc, reasons
	}
	var minSum float64
	for _, d := range ds {
		minSum += d.min
	}
	if minSum > available {
		mins := make([]demand, len(ds))
		for i, d := range ds {
			mins[i] = demand{key: d.key, min: 0, want: d.min, weight: d.weight}
		}
		got := waterFill(mins, available)
		for _, d := range ds {
			alloc[d.key] = math.Max(MinFPS, got[d.key])
			if alloc[d.key] < d.min-0.01 {
				reasons[d.key] = "min_demand_exceeds_capacity"
			} else if d.want > d.min+0.01 {
				reasons[d.key] = "capacity_limited"
			}
		}
		return alloc, reasons
	}
	extra := make([]demand, len(ds))
	for i, d := range ds {
		extra[i] = demand{key: d.key, want: d.want - d.min, weight: d.weight}
	}
	got := waterFill(extra, available-minSum)
	for _, d := range ds {
		alloc[d.key] = d.min + got[d.key]
		if alloc[d.key] < d.want-0.01 {
			reasons[d.key] = "capacity_limited"
		}
	}
	return alloc, reasons
}

// waterFill shares budget over wants by weight; a demand never gets more
// than it wants, and what it leaves is re-shared among the rest.
func waterFill(ds []demand, budget float64) map[string]float64 {
	out := make(map[string]float64, len(ds))
	open := append([]demand(nil), ds...)
	for budget > 1e-9 && len(open) > 0 {
		var w float64
		for _, d := range open {
			w += d.weight
		}
		next := open[:0]
		spent := 0.0
		for _, d := range open {
			share := budget * d.weight / w
			need := d.want - out[d.key]
			if share >= need {
				out[d.key] += need
				spent += need
			} else {
				out[d.key] += share
				spent += share
				next = append(next, d)
			}
		}
		budget -= spent
		if len(next) == len(open) {
			break // everyone took a full share: budget exhausted
		}
		open = next
	}
	return out
}
