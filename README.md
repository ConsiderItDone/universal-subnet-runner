# universal-subnet-runner
This utility is intended for deployment up to ~10 local subnets with default configuration {warp API enabled, 5 nodes, blockchain specified by plugin ID}.

## environment preparation 
 - Create new directory with name "universal-subnet-runner" in the $HOME directory
 - Create directory "avalanchego" in the directory "universal-subnet-runner"
 - Move avalanchego binary to the directory "avalanchego"
 - Create directory "plugins" and put plugin with CB58 name to it

## run utility
To configure and deploy subnet on local environment you should run the command

```sh
go build main.go --vm-name {vm name} --plugin-id {plugin id in CB58 format}
```

```sh
go build main.go --vm-name xsvm --plugin-id v3m4wPxaHpvGr8qfMeyK6PRW3idZrPHmYcMTt7oXdK47yurVH
```
