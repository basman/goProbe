/////////////////////////////////////////////////////////////////////////////////
//
// exclude_management_net_public.go
//
// Written by Fabian Kohn fko@open.ch, February 2016
// Copyright (c) 2016 Open Systems AG, Switzerland
// All Rights Reserved.
//
/////////////////////////////////////////////////////////////////////////////////

// +build !OSAG

package main

func excludeManagementNet(conditional string) string {
    return conditional
}
