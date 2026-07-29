package cal

import "testing"

func TestCheckTXFrequencyAcceptsEveryRegionsAllocations(t *testing.T) {
	// Region 1's 70 cm is 430-440 and Region 2's is 420-450; a check that picked
	// one would refuse legal operation in the other half of the world.
	for _, hz := range []uint32{
		144_500_000, 145_500_000, 147_000_000, // 2 m, all regions
		222_500_000,                           // 1.25 m
		430_100_000, 433_450_000, 438_800_000, // 70 cm, both readings
		447_000_000, // 70 cm, Region 2 only
		927_000_000, // 33 cm
	} {
		if err := CheckTXFrequency(hz); err != nil {
			t.Errorf("CheckTXFrequency(%d) = %v, want nil", hz, err)
		}
	}
}

func TestBandNameLabelsWhatItKnows(t *testing.T) {
	for hz, want := range map[uint32]string{
		438_800_000: "70 cm",
		145_500_000: "2 m",
		98_500_000:  "",
	} {
		if got := BandName(hz); got != want {
			t.Errorf("BandName(%d) = %q, want %q", hz, got, want)
		}
	}
}
