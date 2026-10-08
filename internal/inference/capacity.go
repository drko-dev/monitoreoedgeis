package inference

import "math"

// measureDevice derives device capacity from measured service time: the
// worker is serial, so capacity = 1000 / average latency. Utilization is
// the measured busy fraction (sum of effective FPS x latency).
func measureDevice(measured map[string]Measured, reserve float64) DeviceStatus {
	dev := DeviceStatus{CapacitySource: "unmeasured", ReserveFraction: reserve}
	var latSum, weight, busy float64
	for _, mm := range measured {
		dev.MeasuredThroughput += mm.EffectiveFPS
		if mm.LatencyMS > 0 && mm.EffectiveFPS > 0 {
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

// allocate passes every camera's demand through unchanged. The capacity
// manager (weighted sharing, reserve, saturation) replaces it.
func allocate(ds []demand, available float64, measured bool) (map[string]float64, map[string]string) {
	alloc := make(map[string]float64, len(ds))
	for _, d := range ds {
		alloc[d.key] = d.want
	}
	return alloc, map[string]string{}
}
