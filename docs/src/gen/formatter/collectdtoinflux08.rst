.. Autogenerated by Gollum RST generator (docs/generator/*.go)

CollectdToInflux08
==================

CollectdToInflux08 provides a transformation from collectd JSON data to
InfluxDB 0.8.x compatible JSON data. Trailing and leading commas are removed
from the Collectd message beforehand.



Parameters
----------

**CollectdToInfluxFormatter**
defines the formatter applied before the conversion
from Collectd to InfluxDB. By default this is set to format.Forward.


Example
-------

.. code-block:: yaml

	 - "stream.Broadcast":
	   Formatter: "format.CollectdToInflux08"
	   CollectdToInfluxFormatter: "format.Forward"
	


