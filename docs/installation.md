# Installation guide

## Dependencies

* Cassandra. We run and recommend 3.0.8 . See [Cassandra](https://github.com/raintank/metrictank/blob/master/docs/cassandra.md)
* Elasticsearch is currently a dependency for metrics metadata, but we will remove this soon.  See [metadata in ES](https://github.com/raintank/metrictank/blob/master/docs/metadata.md#es)
* Optionally, a queue if you want to buffer data in case metrictank goes down: Kafka 0.10 is recommended, but 0.9 should work too.
* Currently you also need the [graphite-raintank finder plugin](https://github.com/raintank/graphite-metrictank) and our [graphite-api fork](https://github.com/raintank/graphite-api/) (which we install as 1 piece).
* [statsd](https://github.com/etsy/statsd) or something compatible with it.  For instrumentation

We'll go over these in more detail below.

## How things fit together

Metrictank ingests metrics data. The data can be sent into it, or be read from a queue (see [Inputs](https://github.com/raintank/metrictank/blob/master/docs/inputs.md)). Metrictank will compress the data into chunks in RAM, a configurable amount of the most recent data is kept in RAM, but the chunks are being saved to Cassandra as well. You can use a single Cassandra instance or a cluster. Metrictank will also respond to queries: if the data is recent, it'll come out of RAM, and older data is fetched from cassandra. This happens transparantly. Metrictank also needs elasticsearch to maintain an index of metrics metadata. You'll typically query metrictank by querying graphite-api which uses the graphite-metrictank plugin to talk to metrictank. You can also query metrictank directly but this is very limited, experimental and not recommended.


## Installation

### From source

Building metrictank requires a [Golang](https://golang.org/) compiler.
We recommend version 1.5 or higher.

```
go get github.com/raintank/metrictank
```

This installs only metrictank itself, and none of its dependencies.

### Distribution packages

We automatically build rpms and debs on circleCi for all needed components whenever the build succeeds. These packages are pushed to packagecloud.

[Instructions to enable the raintank packagecloud repository](https://packagecloud.io/raintank/raintank/install)

You need to install these packages:

* metrictank
* graphite-metrictank (includes both our graphite-api variant as well as the graphite-metrictank finder plugin)

Releases are simply tagged versions like `0.5.1` ([releases](https://github.com/raintank/metrictank/releases)), whereas commits in master following a release will be named `version-commit-after` for example `0.5.1-20` for the 20th commit after `0.5.1`

We aim to keep master stable, so that's your best bet.

Supported distributions:

* Ubuntu 14.04 (Trusty Tahr), 16.04 (Xenial Xerus)
* Debian 7 (wheezy), 8 (jessie)
* Centos 6, 7

### Chef cookbook

[chef_metric_tank](https://github.com/raintank/chef_metric_tank)

This installs only metrictank itself, and none of its dependencies.

## Set up cassandra

For basic setups, you can just install it and start it with default settings.
To tweak schema and settings, see [Cassandra](https://github.com/raintank/metrictank/blob/master/docs/cassandra.md)

## Set up elasticsearch

Here, you can also just install it and start it with default settings. 

## Set up statsd

Metrictank needs statsd or a statsd-compatible agent for its instrumentation.
It will refuse to start if nothing listens on the configured `statsd-addr`.

You can install the official [statsd](https://github.com/etsy/statsd) (see its installation instructions)
or an alternative. We recommend [vimeo/statsdaemon](https://github.com/vimeo/statsdaemon).

For the [metrictank dashboard](https://grafana.net/dashboards/279) to work properly, you need the right statsd/statsdaemon settings.

Below are instructions for statsd and statsdaemon:

Note:
 * `<environment>` is however you choose to call your environment. (test, production, dev, ...).
 * we recommend installing statsd/statsdaemon on the same host as metrictank.

### Statsdaemon

[Statsdaemon](https://github.com/vimeo/statsdaemon) is the recommended option.
To install it, you can either use the deb packages from the aforementioned repository,
or you need to have a [Golang](https://golang.org/) compiler installed.
In that case just run `go get github.com/Vimeo/statsdaemon/statsdaemon`

Get the default config file from `https://github.com/vimeo/statsdaemon/blob/master/statsdaemon.ini`
and update the following settings:

```
flush_interval = 1
prefix_rates = "stats.<environment>."
prefix_timers = "stats.<environment>.timers."
prefix_gauges = "stats.<environment>.gauges."

percentile_thresholds = "90,75"
```

Also, since by default metrictank listens on port 6060, you'll need to change statsdaemon's `profile_addr` setting from `":6060"` to something else, like `":6061"`.

Then just run `statsdaemon`.  If you use ubuntu you can use the package or the [upstart init config](https://github.com/vimeo/statsdaemon/blob/master/upstart-init-statsdaemon.conf) from the statsdaemon repo.

### Statsd

See the instructions on the [statsd homepage](https://github.com/etsy/statsd)
Set the following options:

```
flushInterval: 1000
globalPrefix: "stats.<environment>"
```

## Optional: set up kafka

You can run a persistent queue in front of metrictank.
If your metric instance(s) go down, then a queue is helpful in buffering and saving all the data while your instance(s) is/are down.
The moment your metrictank instance(s) come(s) back up, they can replay everything they missed (and more, it's useful to load in older data
so that you can serve queries for it out of RAM).
Also, in case you want to make any change to your aggregations, Cassandra cluster, or whatever, it can be useful to re-process older data.

** Note: the above actually doesn't work yet, as we don't have the seek-back-in-time implemented yet to fetch old data from Kafka.
So for now using Kafka is more about preparing for the future than getting immediate benefit. **

You can install Kafka (and its zookeeper dependency). Ideally, it should be 0.10 or later. Then, just run it.  Default settings are fine.

## Configuration

See the [example config file](https://github.com/raintank/metrictank/blob/master/metrictank-sample.ini) which guides you through the various options.

You may need to adjust the `statsd-addr` based on where you decided to run that service.

Out of the box, one input is enabled: the [Carbon line input](https://github.com/raintank/metrictank/blob/master/docs/inputs.md#carbon)
It uses a default storage-schemas to coalesce every incoming metric into 1 second resolution.  You may want to fine tune this for your needs.
(or simply what you already use in a pre-existing Graphite install).
See the input plugin documentation referenced above for more details.

If you want to use Kafka, you should enable the Kafka-mdm input plugin.  See [the Inputs docs for more details](https://github.com/raintank/metrictank/blob/master/docs/inputs.md).
See the `kafka-mdm-in` section in the config for the options you need to tweak.

## Run it!

If using upstart:
```
service metrictank start
```

If using systemd:
```
systemctl start metrictank
```

Note that metrictank simply logs to stdout.  So where the log data ends up depends on your init system.

If using upstart, you can then find the logs at `/var/log/upstart/metrictank.log`.
With systemd, you can use something like `journalctl -f metrictank`.
