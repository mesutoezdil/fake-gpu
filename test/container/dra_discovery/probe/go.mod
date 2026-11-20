// The probe is built in GOPATH mode by run.sh, next to a copy of AMD's
// discovery package. This file only keeps it out of the main module, so
// go mod tidy does not pull in AMD's driver and its dependencies.
module probe

go 1.24
