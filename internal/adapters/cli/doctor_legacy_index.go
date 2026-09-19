package cli

// doctorLegacyIndexMarker is the DOCTOR-CHECK-* vocabulary entry for a
// pre-rename session index left at the sessions root — see
// doctorCheckLegacyIndex.
const doctorLegacyIndexMarker = "DOCTOR-CHECK-LEGACY-INDEX-y5"

func doctorCheckLegacyIndex() doctorCheck {
	return doctorCheck{Marker: doctorLegacyIndexMarker, Status: doctorOK, Detail: "not examined"}
}
