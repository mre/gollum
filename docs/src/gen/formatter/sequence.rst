.. Autogenerated by Gollum RST generator (docs/generator/*.go)

Sequence
========

Sequence is a formatter that allows prefixing a message with the message's
sequence number



Parameters
----------

**Separator**
sets the separator character placed after the sequence
number. This is set to ":" by default. If no separator is set the sequence string will only set.


**ApplyTo**
defines the formatter content to use


Example
-------

.. code-block:: yaml

	 - format.Sequence:
	     Separator: ":"
	     ApplyTo: "payload" # payload or <metaKey>
	


