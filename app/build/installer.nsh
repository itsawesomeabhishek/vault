!include LogicLib.nsh

!define VAULT_NODE "$INSTDIR\resources\bin\vault-node.exe"

!macro customInstall
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Vault node RPC"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Vault gossip TCP"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Vault gossip UDP"'
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="Vault node RPC" dir=in action=allow protocol=TCP localport=19000-19001 program="${VAULT_NODE}" enable=yes'
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="Vault gossip TCP" dir=in action=allow protocol=TCP localport=17946-17947 program="${VAULT_NODE}" enable=yes'
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="Vault gossip UDP" dir=in action=allow protocol=UDP localport=17946-17947 program="${VAULT_NODE}" enable=yes'
!macroend

!macro customUnInstall
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Vault node RPC"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Vault gossip TCP"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Vault gossip UDP"'
!macroend
