package libbox

// ProbeOutbound returns only after a bounded HTTP check through the active
// outbound. Call from a worker queue; the service lifetime cancels pending I/O.
func (s *CommandServer) ProbeOutbound(outboundTag string, timeoutMilliseconds int32) (bool, error) {
	return s.StartedService.ProbeOutbound(outboundTag, timeoutMilliseconds)
}
