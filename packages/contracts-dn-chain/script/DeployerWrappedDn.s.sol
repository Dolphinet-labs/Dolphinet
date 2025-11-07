// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "forge-std/Vm.sol";
import {console, Script} from "forge-std/Script.sol";

import {WDN} from "../src/core/token/WDN.sol";

contract DeployerWrappedFdScript is Script {
    WDN public wDN;

    function run() public {
        vm.startBroadcast();
        wDN = new WDN();
        console.log("deploy wDn:", address(wDN));
        console.log("wDN", wDN.name());
        vm.stopBroadcast();
    }
}
