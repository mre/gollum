.. Autogenerated by Gollum RST generator (docs/generator/*.go)

Stream
======

This plugin filters messages by stream based on a black and a whitelist.
The blacklist is checked first.



Parameters
----------

**FilterBlockStreams**
sets a list of streams that are blocked. If a message's
stream is not in that list, the OnlyStreams list is tested. This list ist
empty by default.


**FilterOnlyStreams**
sets a list of streams that may pass. Messages from streams
that are not in this list are blocked unless the list is empty. By default
this list is empty.


Parameters (from SimpleFilter)
------------------------------

**FilteredStream**
defines a stream where filtered messages get sent to.
You can disable this behavior by setting "". Set to "" by default.


Example
-------

.. code-block:: yaml

	 - "stream.Broadcast":
	   Filter: "filter.Stream"
	   FilterBlockStreams:
	     - "foo"
	   FilterOnlyStreams:
	     - "test1"
	     - "test2"
	


