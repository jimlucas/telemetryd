# Protocol and mapping sources

The implementation was derived from public protocol and source references rather than reverse-engineering a proprietary binary.

- OpenConfig gNMI specification and protobuf:
  - https://github.com/openconfig/reference/blob/master/rpc/gnmi/gnmi-specification.md
  - https://github.com/openconfig/gnmi/blob/master/proto/gnmi/gnmi.proto
- Public gNMIc dial-out listener implementation:
  - https://github.com/openconfig/gnmic/blob/f8353cbec006a1d592d55de3a7b78bf51aa04700/pkg/cmd/listener/listener.go
- Public Nokia SROS-style dial-out service package used by gNMIc:
  - https://github.com/karimra/sros-dialout
- Minimal public mock showing the exact method path and empty response:
  - https://github.com/mabra94/sros_gnmi_dialOut_mockServer/blob/1a61566ab6464fba5219b54c3460ce19a225d55a/main.go
- Public Tarana gNMIc agent example and observed BN/RN path mappings:
  - https://github.com/insightfinder/InsightAgent/tree/411a562254bada316395a8ef3ddece9b2c8bff57/tarana-gnmic-agent

The `proto/tarana_dialout.proto` file in this project is documentation-only. The listener registers the method dynamically so it does not need generated code for the vendor service wrapper.
