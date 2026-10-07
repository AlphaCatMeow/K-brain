package browser

import (
	"encoding/json"
	"fmt"
)

// Native setters bypass framework value trackers and preserve non-ASCII text.
func fillExpression(selector, text string) string {
	sel, _ := json.Marshal(selector)
	value, _ := json.Marshal(text)
	return fmt.Sprintf(`(()=>{
const e=document.querySelector(%s);
if(!e)throw new Error('fill: element not found');
if(e.disabled||e.readOnly)throw new Error('fill: element is disabled or readonly');
const value=%s;
e.focus({preventScroll:true});
if(e instanceof HTMLInputElement||e instanceof HTMLTextAreaElement){
 const proto=e instanceof HTMLTextAreaElement?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;
 Object.getOwnPropertyDescriptor(proto,'value').set.call(e,value);
}else if(e.isContentEditable){e.textContent=value;}
else{throw new Error('fill: element is not editable');}
e.dispatchEvent(new InputEvent('input',{bubbles:true,inputType:'insertText',data:value}));
e.dispatchEvent(new Event('change',{bubbles:true}));
return true;
})()`, sel, value)
}
