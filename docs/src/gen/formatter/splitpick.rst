.. Autogenerated by Gollum RST generator (docs/generator/*.go)

SplitPick
=========

SplitPick separates value of messages according to a specified delimiter
and returns the given indexed message. The index are zero based.




Parameters
----------

**SplitPickIndex**
defaults to 0.


**SplitPickDelimiter**
defaults to  ":".


**ApplyTo**
defines the formatter content to use


Example
-------

.. code-block:: yaml

	 - format.SplitPick:
		  Index: 0
		  Delimiter: ":"
		  ApplyTo: "payload" # payload or <metaKey>
	


