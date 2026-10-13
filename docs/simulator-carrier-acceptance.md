# Deferred carrier acceptance

V1 is implemented for an isolated inbound SIP receiver. Nothing in this document starts or changes the existing Mac, ThinkPad, Active Call service, SIM gateway or carrier route.

Before connecting Claude's voice assistant to a telephone number:

1. Identify the gateway model and independently routable voice channels with the previously prepared read-only inventory. An idle API or spare SIM slot is not proof that two simultaneous voice legs work.
2. Allocate a dedicated receiver host/network, inbound number or supported SIM channel, and separate provider capacity. Keep existing services and routing unchanged.
3. Verify how the carrier/gateway preserves the prepared run identity in the inbound SIP called extension. V1 requires `sip:<run_id>@<configured domain>`; it never assigns a call to whichever run is current. If the fixed-number gateway cannot do this, implement an explicit reservation-bound route before claiming carrier support.
4. Prepare an exact deployment manifest for the dedicated host: private HTTP/media addresses, SIP transport, advertised SDP address, bounded RTP range, codec, authentication and firewall rules. Review that concrete manifest before shared-infrastructure changes.
5. With the deployed destination authorized, place one short cooperative call, one confused-customer call and one interruption call. Use synthetic identities. No automatic retries or campaign dialing.
6. Capture answer, greeting, both recognizable audio directions, interruption, BYE/CANCEL and cleanup. Mark provider failures and missing responses. Audio boundaries on the receiver do not prove handset playout; use a handset/audio fixture for acoustic claims.
7. Compare the application's report to the call evidence. Three calls do not establish a useful p95. Semantic checks remain indeterminate until independently evaluated.
8. Rollback names only the new receiver project and its dedicated route/resources. Never run a broad Docker prune, kill a process by a shared name, replace the existing gateway configuration, or reuse its credentials to save setup time.

Physical phones are optional for the initial SIP transport test. They become useful for validating the actual carrier and handset audio path, not for providing unattended call pickup: the dedicated SIP gateway controls answer and media.
