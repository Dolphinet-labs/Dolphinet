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

contract DeployerCpChainBridge is Script {
    EmptyContract public emptyContract;
    ProxyAdmin public chainBaseProxyAdmin;
    FdChainBase public chainBase;
    FdChainBase public chainBaseImplementation;

    ProxyAdmin public pauserRegistryProxyAdmin;
    PauserRegistry public pauserRegistry;
    PauserRegistry public pauserRegistryImplementation;

    ProxyAdmin public chainDepositManagerProxyAdmin;
    FdChainDepositManager public chainDepositManager;
    FdChainDepositManager public chainDepositManagerImplementation;

    ProxyAdmin public delegationManagerProxyAdmin;
    DelegationManager public delegationManager;
    DelegationManager public delegationManagerImplementation;

    ProxyAdmin public slashingManagerProxyAdmin;
    SlashingManager public slashingManager;
    SlashingManager public slashingManagerImplementation;

    ProxyAdmin public governanceManagerProxyAdmin;
    DolphinetGovernance public governanceManager;
    DolphinetGovernance public governanceManagerImplementation;

    ProxyAdmin public rewardManagerProxyAdmin;
    RewardManager public rewardManager;
    RewardManager public rewardManagerImplementation;

    address[] public pausers;

    function run() public {
        uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");
        // uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");
        address deployerAddress = vm.addr(deployerPrivateKey);

        pausers = [deployerAddress, deployerAddress];

        vm.startBroadcast(deployerPrivateKey);

        emptyContract = new EmptyContract();

        TransparentUpgradeableProxy proxyChainBase = new TransparentUpgradeableProxy(
                address(emptyContract),
                deployerAddress,
                ""
            );
        chainBase = FdChainBase(payable(address(proxyChainBase)));
        chainBaseImplementation = new FdChainBase();
        chainBaseProxyAdmin = ProxyAdmin(
            getProxyAdminAddress(address(proxyChainBase))
        );

        TransparentUpgradeableProxy proxyChainDepositManager = new TransparentUpgradeableProxy(
                address(emptyContract),
                deployerAddress,
                ""
            );
        chainDepositManager = FdChainDepositManager(
            payable(address(proxyChainDepositManager))
        );
        chainDepositManagerImplementation = new FdChainDepositManager();
        chainDepositManagerProxyAdmin = ProxyAdmin(
            getProxyAdminAddress(address(proxyChainDepositManager))
        );

        TransparentUpgradeableProxy proxyPauserRegistry = new TransparentUpgradeableProxy(
                address(emptyContract),
                deployerAddress,
                ""
            );
        pauserRegistry = PauserRegistry(payable(address(proxyPauserRegistry)));
        pauserRegistryImplementation = new PauserRegistry(
            pausers,
            deployerAddress
        );
        pauserRegistryProxyAdmin = ProxyAdmin(
            getProxyAdminAddress(address(proxyPauserRegistry))
        );

        TransparentUpgradeableProxy proxyDelegationManager = new TransparentUpgradeableProxy(
                address(emptyContract),
                deployerAddress,
                ""
            );
        delegationManager = DelegationManager(
            payable(address(proxyDelegationManager))
        );
        delegationManagerImplementation = new DelegationManager();
        delegationManagerProxyAdmin = ProxyAdmin(
            getProxyAdminAddress(address(proxyDelegationManager))
        );

        TransparentUpgradeableProxy proxySlashingManager = new TransparentUpgradeableProxy(
                address(emptyContract),
                deployerAddress,
                ""
            );
        slashingManager = SlashingManager(
            payable(address(proxySlashingManager))
        );
        slashingManagerImplementation = new SlashingManager();
        slashingManagerProxyAdmin = ProxyAdmin(
            getProxyAdminAddress(address(proxySlashingManager))
        );

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

        TransparentUpgradeableProxy proxyRewardManager = new TransparentUpgradeableProxy(
                address(emptyContract),
                deployerAddress,
                ""
            );
        rewardManager = RewardManager(payable(address(proxyRewardManager)));
        rewardManagerImplementation = new RewardManager();
        rewardManagerProxyAdmin = ProxyAdmin(
            getProxyAdminAddress(address(proxyRewardManager))
        );

        chainBaseProxyAdmin.upgradeAndCall(
            ITransparentUpgradeableProxy(address(chainBase)),
            address(chainBaseImplementation),
            abi.encodeWithSelector(
                FdChainBase.initialize.selector,
                address(pauserRegistry),
                320000 * 1e18,
                1820000 * 1e18,
                address(chainDepositManager)
            )
        );

        pauserRegistryProxyAdmin.upgradeAndCall(
            ITransparentUpgradeableProxy(address(pauserRegistry)),
            address(pauserRegistryImplementation),
            ""
        );

        chainDepositManagerProxyAdmin.upgradeAndCall(
            ITransparentUpgradeableProxy(address(chainDepositManager)),
            address(chainDepositManagerImplementation),
            abi.encodeWithSelector(
                FdChainDepositManager.initialize.selector,
                deployerAddress,
                address(chainBase),
                address(delegationManager)
            )
        );

        delegationManagerProxyAdmin.upgradeAndCall(
            ITransparentUpgradeableProxy(address(delegationManager)),
            address(delegationManagerImplementation),
            abi.encodeWithSelector(
                DelegationManager.initialize.selector,
                deployerAddress,
                address(pauserRegistry),
                0,
                20, // delayWithdrawalBlocks
                address(chainDepositManager),
                address(chainBase),
                address(slashingManager),
                address(governanceManager)
            )
        );

        slashingManagerProxyAdmin.upgradeAndCall(
            ITransparentUpgradeableProxy(address(slashingManager)),
            address(slashingManagerImplementation),
            abi.encodeWithSelector(
                SlashingManager.initialize.selector,
                deployerAddress,
                address(delegationManager),
                address(slashingManager),
                0, // min withdrawal amount
                deployerAddress // slashing recipient
            )
        );

        governanceManagerProxyAdmin.upgradeAndCall(
            ITransparentUpgradeableProxy(address(governanceManager)),
            address(governanceManagerImplementation),
            abi.encodeWithSelector(
                DolphinetGovernance.initialize.selector,
                address(delegationManager),
                address(delegationManager)
            )
        );

        rewardManagerProxyAdmin.upgradeAndCall(
            ITransparentUpgradeableProxy(address(rewardManager)),
            address(rewardManagerImplementation),
            abi.encodeWithSelector(
                RewardManager.initialize.selector,
                deployerAddress,
                deployerAddress,
                deployerAddress,
                50, // stake percent
                address(pauserRegistry),
                address(delegationManager),
                address(chainDepositManager)
            )
        );

        console.log("deploy proxyChainBase:", address(proxyChainBase));
        console.log(
            "Implementation ChainBase:",
            address(chainBaseImplementation)
        );

        console.log(
            "deploy proxyPauserRegistry:",
            address(proxyPauserRegistry)
        );
        console.log(
            "Implementation PauserRegistry:",
            address(pauserRegistryImplementation)
        );
        console.log(
            "deploy proxyChainDepositManager:",
            address(proxyChainDepositManager)
        );
        console.log(
            "Implementation ChainDepositManager:",
            address(chainDepositManagerImplementation)
        );

        console.log(
            "deploy proxyDelegationManager:",
            address(proxyDelegationManager)
        );
        console.log(
            "Implementation DelegationManager:",
            address(delegationManagerImplementation)
        );

        console.log(
            "deploy proxySlashingManager:",
            address(proxySlashingManager)
        );
        console.log(
            "Implementation SlashingManager:",
            address(slashingManagerImplementation)
        );

        console.log(
            "deploy proxyGovernanceManager:",
            address(proxyGovernanceManager)
        );
        console.log(
            "Implementation GovernanceManager:",
            address(governanceManagerImplementation)
        );

        console.log("deploy proxyRewardManager:", address(proxyRewardManager));
        console.log(
            "Implementation RewardManager:",
            address(rewardManagerImplementation)
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
