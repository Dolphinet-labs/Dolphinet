// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.20;

import "forge-std/Vm.sol";
import {Script, console} from "forge-std/Script.sol";
import "@openzeppelin/contracts/proxy/transparent/ProxyAdmin.sol";
import "@openzeppelin/contracts/proxy/transparent/TransparentUpgradeableProxy.sol";

import "../../src/interfaces/IPauserRegistry.sol";
import "../../src/interfaces/IFdChainDepositManager.sol";

import {EmptyContract} from "../utils/EmptyContract.sol";
import {FdChainBase} from "../../src/core/pos/ChainBase.sol";
import {FdChainDepositManager} from "../../src/core/pos/ChainDepositManager.sol";
import {DelegationManager} from "../../src/core/pos/DelegationManager.sol";
import {SlashingManager} from "../../src/core/pos/SlashingManager.sol";
import {RewardManager} from "../../src/core/pos/RewardManager.sol";
import {DolphinetGovernance} from "../../src/core/pos/Governance.sol";
import {PauserRegistry} from "../../src/access/PauserRegistry.sol";

contract DeployerDol is Script {
    EmptyContract public emptyContract;

    ProxyAdmin public governanceManagerProxyAdmin;
    DolphinetGovernance public governanceManager;
    DolphinetGovernance public governanceManagerImplementation;

    function run() public {
        uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");
        // uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");
        address deployerAddress = vm.addr(deployerPrivateKey);

        vm.startBroadcast(deployerPrivateKey);

        emptyContract = new EmptyContract();

        TransparentUpgradeableProxy proxyGovernanceManager = new TransparentUpgradeableProxy(
                address(emptyContract),
                deployerAddress,
                ""
            );
        governanceManager = DolphinetGovernance(
            payable(address(proxyGovernanceManager))
        );
        governanceManagerImplementation = new DolphinetGovernance();
        governanceManagerProxyAdmin = ProxyAdmin(
            getProxyAdminAddress(address(proxyGovernanceManager))
        );

        governanceManagerProxyAdmin.upgradeAndCall(
            ITransparentUpgradeableProxy(address(governanceManager)),
            address(governanceManagerImplementation),
            abi.encodeWithSelector(
                DolphinetGovernance.initialize.selector,
                deployerAddress,
                deployerAddress,
                address(0),
                address(0)
            )
        );

        console.log(
            "deploy proxyGovernanceManager:",
            address(proxyGovernanceManager)
        );
        console.log(
            "Implementation GovernanceManager:",
            address(governanceManagerImplementation)
        );
    }

    function getProxyAdminAddress(
        address proxy
    ) internal view returns (address) {
        address CHEATCODE_ADDRESS = 0x7109709ECfa91a80626fF3989D68f67F5b1DD12D;
        Vm vm = Vm(CHEATCODE_ADDRESS);

        bytes32 adminSlot = vm.load(proxy, ERC1967Utils.ADMIN_SLOT);
        return address(uint160(uint256(adminSlot)));
    }
}
